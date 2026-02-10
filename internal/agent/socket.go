package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/thobiasn/rook/internal/protocol"
)

// SocketServer serves protocol messages over a Unix domain socket.
type SocketServer struct {
	hub      *Hub
	store    *Store
	docker   *DockerCollector
	alerter  *Alerter
	listener net.Listener
	wg       sync.WaitGroup
}

// NewSocketServer creates a SocketServer. Call Start to begin accepting connections.
func NewSocketServer(hub *Hub, store *Store, docker *DockerCollector, alerter *Alerter) *SocketServer {
	return &SocketServer{
		hub:     hub,
		store:   store,
		docker:  docker,
		alerter: alerter,
	}
}

// Start begins listening on the given Unix socket path.
func (ss *SocketServer) Start(path string) error {
	// Remove stale socket file.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	// Set permissions to 0660.
	if err := os.Chmod(path, 0660); err != nil {
		ln.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	ss.listener = ln
	ss.wg.Add(1)
	go ss.acceptLoop()
	slog.Info("socket server started", "path", path)
	return nil
}

// Stop closes the listener and waits for all connections to finish.
func (ss *SocketServer) Stop() {
	if ss.listener != nil {
		ss.listener.Close()
	}
	ss.wg.Wait()
	slog.Info("socket server stopped")
}

func (ss *SocketServer) acceptLoop() {
	defer ss.wg.Done()
	for {
		conn, err := ss.listener.Accept()
		if err != nil {
			if !isClosedErr(err) {
				slog.Error("accept error", "error", err)
			}
			return
		}
		ss.wg.Add(1)
		go ss.handleConn(conn)
	}
}

func (ss *SocketServer) handleConn(conn net.Conn) {
	defer ss.wg.Done()
	defer conn.Close()

	c := &connState{
		ss:   ss,
		conn: conn,
		subs: make(map[string]*subscription),
	}
	defer c.cleanup()

	for {
		env, err := protocol.ReadMsg(conn)
		if err != nil {
			if !isEOF(err) && !isClosedErr(err) {
				slog.Warn("read error", "error", err)
			}
			return
		}
		c.dispatch(env)
	}
}

type subscription struct {
	sub    *subscriber
	topic  string
	cancel context.CancelFunc
}

// connState holds per-connection state.
type connState struct {
	ss      *SocketServer
	conn    net.Conn
	writeMu sync.Mutex
	subs    map[string]*subscription // topic -> subscription
}

func (c *connState) cleanup() {
	for topic, s := range c.subs {
		s.cancel()
		c.ss.hub.Unsubscribe(s.topic, s.sub)
		delete(c.subs, topic)
	}
}

func (c *connState) writeMsg(env *protocol.Envelope) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := protocol.WriteMsg(c.conn, env); err != nil {
		if !isClosedErr(err) {
			slog.Warn("write error", "error", err)
		}
	}
}

func (c *connState) sendResult(id uint32, res *protocol.Result) {
	env, err := protocol.NewEnvelope(protocol.TypeResult, id, res)
	if err != nil {
		slog.Error("encode result", "error", err)
		return
	}
	c.writeMsg(env)
}

func (c *connState) sendError(id uint32, msg string) {
	env, err := protocol.NewEnvelope(protocol.TypeError, id, &protocol.ErrorResult{Error: msg})
	if err != nil {
		slog.Error("encode error", "error", err)
		return
	}
	c.writeMsg(env)
}

func (c *connState) sendResponse(id uint32, typ protocol.MsgType, body any) {
	env, err := protocol.NewEnvelope(typ, id, body)
	if err != nil {
		slog.Error("encode response", "error", err)
		return
	}
	c.writeMsg(env)
}

func (c *connState) dispatch(env *protocol.Envelope) {
	switch env.Type {
	// Streaming subscriptions.
	case protocol.TypeSubscribeMetrics:
		c.subscribeMetrics(env.ID)
	case protocol.TypeSubscribeLogs:
		c.subscribeLogs(env)
	case protocol.TypeSubscribeAlerts:
		c.subscribeAlerts(env.ID)
	case protocol.TypeUnsubscribe:
		c.unsubscribe(env)

	// Queries.
	case protocol.TypeQueryMetrics:
		c.queryMetrics(env)
	case protocol.TypeQueryLogs:
		c.queryLogs(env)
	case protocol.TypeQueryAlerts:
		c.queryAlerts(env)
	case protocol.TypeQueryContainers:
		c.queryContainers(env.ID)

	// Actions.
	case protocol.TypeActionAckAlert:
		c.ackAlert(env)
	case protocol.TypeActionSilence:
		c.silenceAlert(env)
	case protocol.TypeActionRestart:
		c.restartContainer(env)

	default:
		c.sendError(env.ID, fmt.Sprintf("unknown message type: %s", env.Type))
	}
}

// --- Streaming ---

func (c *connState) subscribeMetrics(id uint32) {
	if _, exists := c.subs[TopicMetrics]; exists {
		return
	}

	sub, ch := c.ss.hub.Subscribe(TopicMetrics)
	ctx, cancel := context.WithCancel(context.Background())
	c.subs[TopicMetrics] = &subscription{sub: sub, topic: TopicMetrics, cancel: cancel}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				update, ok := msg.(*protocol.MetricsUpdate)
				if !ok {
					continue
				}
				env, err := protocol.NewEnvelope(protocol.TypeMetricsUpdate, 0, update)
				if err != nil {
					continue
				}
				c.writeMsg(env)
			}
		}
	}()
}

func (c *connState) subscribeLogs(env *protocol.Envelope) {
	if _, exists := c.subs[TopicLogs]; exists {
		return
	}

	var filter protocol.SubscribeLogs
	if env.Body != nil {
		protocol.DecodeBody(env.Body, &filter)
	}

	// Resolve project to container IDs if specified.
	var projectIDs map[string]bool
	if filter.Project != "" {
		projectIDs = make(map[string]bool)
		for _, ctr := range c.ss.docker.Containers() {
			if ctr.Project == filter.Project {
				projectIDs[ctr.ID] = true
			}
		}
	}

	sub, ch := c.ss.hub.Subscribe(TopicLogs)
	ctx, cancel := context.WithCancel(context.Background())
	c.subs[TopicLogs] = &subscription{sub: sub, topic: TopicLogs, cancel: cancel}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				entry, ok := msg.(*protocol.LogEntryMsg)
				if !ok {
					continue
				}
				// Apply filters.
				if filter.ContainerID != "" && entry.ContainerID != filter.ContainerID {
					continue
				}
				if projectIDs != nil && !projectIDs[entry.ContainerID] {
					continue
				}
				if filter.Stream != "" && entry.Stream != filter.Stream {
					continue
				}
				if filter.Search != "" && !strings.Contains(entry.Message, filter.Search) {
					continue
				}
				env, err := protocol.NewEnvelope(protocol.TypeLogEntry, 0, entry)
				if err != nil {
					continue
				}
				c.writeMsg(env)
			}
		}
	}()
}

func (c *connState) subscribeAlerts(id uint32) {
	if _, exists := c.subs[TopicAlerts]; exists {
		return
	}

	sub, ch := c.ss.hub.Subscribe(TopicAlerts)
	ctx, cancel := context.WithCancel(context.Background())
	c.subs[TopicAlerts] = &subscription{sub: sub, topic: TopicAlerts, cancel: cancel}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				event, ok := msg.(*protocol.AlertEvent)
				if !ok {
					continue
				}
				env, err := protocol.NewEnvelope(protocol.TypeAlertEvent, 0, event)
				if err != nil {
					continue
				}
				c.writeMsg(env)
			}
		}
	}()
}

func (c *connState) unsubscribe(env *protocol.Envelope) {
	var unsub protocol.Unsubscribe
	if err := protocol.DecodeBody(env.Body, &unsub); err != nil {
		c.sendError(env.ID, "invalid unsubscribe body")
		return
	}

	if s, exists := c.subs[unsub.Topic]; exists {
		s.cancel()
		c.ss.hub.Unsubscribe(s.topic, s.sub)
		delete(c.subs, unsub.Topic)
	}
}

// --- Queries ---

func (c *connState) queryMetrics(env *protocol.Envelope) {
	var req protocol.QueryMetricsReq
	if err := protocol.DecodeBody(env.Body, &req); err != nil {
		c.sendError(env.ID, "invalid query body")
		return
	}

	ctx := context.Background()
	host, err := c.ss.store.QueryHostMetrics(ctx, req.Start, req.End)
	if err != nil {
		c.sendError(env.ID, fmt.Sprintf("query host metrics: %v", err))
		return
	}
	disks, err := c.ss.store.QueryDiskMetrics(ctx, req.Start, req.End)
	if err != nil {
		c.sendError(env.ID, fmt.Sprintf("query disk metrics: %v", err))
		return
	}
	nets, err := c.ss.store.QueryNetMetrics(ctx, req.Start, req.End)
	if err != nil {
		c.sendError(env.ID, fmt.Sprintf("query net metrics: %v", err))
		return
	}
	containers, err := c.ss.store.QueryContainerMetrics(ctx, req.Start, req.End)
	if err != nil {
		c.sendError(env.ID, fmt.Sprintf("query container metrics: %v", err))
		return
	}

	resp := protocol.QueryMetricsResp{
		Host:       convertTimedHost(host),
		Disks:      convertTimedDisk(disks),
		Networks:   convertTimedNet(nets),
		Containers: convertTimedContainer(containers),
	}
	c.sendResponse(env.ID, protocol.TypeResult, &resp)
}

func (c *connState) queryLogs(env *protocol.Envelope) {
	var req protocol.QueryLogsReq
	if err := protocol.DecodeBody(env.Body, &req); err != nil {
		c.sendError(env.ID, "invalid query body")
		return
	}

	filter := LogFilter{
		Start:  req.Start,
		End:    req.End,
		Stream: req.Stream,
		Search: req.Search,
		Limit:  req.Limit,
	}
	if req.ContainerID != "" {
		filter.ContainerIDs = []string{req.ContainerID}
	}

	entries, err := c.ss.store.QueryLogs(context.Background(), filter)
	if err != nil {
		c.sendError(env.ID, fmt.Sprintf("query logs: %v", err))
		return
	}

	resp := protocol.QueryLogsResp{Entries: convertLogEntries(entries)}
	c.sendResponse(env.ID, protocol.TypeResult, &resp)
}

func (c *connState) queryAlerts(env *protocol.Envelope) {
	var req protocol.QueryAlertsReq
	if err := protocol.DecodeBody(env.Body, &req); err != nil {
		c.sendError(env.ID, "invalid query body")
		return
	}

	alerts, err := c.ss.store.QueryAlerts(context.Background(), req.Start, req.End)
	if err != nil {
		c.sendError(env.ID, fmt.Sprintf("query alerts: %v", err))
		return
	}

	resp := protocol.QueryAlertsResp{Alerts: convertAlerts(alerts)}
	c.sendResponse(env.ID, protocol.TypeResult, &resp)
}

func (c *connState) queryContainers(id uint32) {
	containers := c.ss.docker.Containers()
	resp := protocol.QueryContainersResp{
		Containers: make([]protocol.ContainerInfo, len(containers)),
	}
	for i, ctr := range containers {
		resp.Containers[i] = protocol.ContainerInfo{
			ID:      ctr.ID,
			Name:    ctr.Name,
			Image:   ctr.Image,
			State:   ctr.State,
			Project: ctr.Project,
		}
	}
	c.sendResponse(id, protocol.TypeResult, &resp)
}

// --- Actions ---

func (c *connState) ackAlert(env *protocol.Envelope) {
	var req protocol.AckAlertReq
	if err := protocol.DecodeBody(env.Body, &req); err != nil {
		c.sendError(env.ID, "invalid body")
		return
	}
	if err := c.ss.store.AckAlert(context.Background(), req.AlertID); err != nil {
		c.sendError(env.ID, err.Error())
		return
	}
	c.sendResult(env.ID, &protocol.Result{OK: true, Message: "acknowledged"})
}

func (c *connState) silenceAlert(env *protocol.Envelope) {
	var req protocol.SilenceAlertReq
	if err := protocol.DecodeBody(env.Body, &req); err != nil {
		c.sendError(env.ID, "invalid body")
		return
	}
	if c.ss.alerter == nil {
		c.sendError(env.ID, "alerter not configured")
		return
	}
	c.ss.alerter.Silence(req.RuleName, time.Duration(req.Duration)*time.Second)
	c.sendResult(env.ID, &protocol.Result{OK: true, Message: "silenced"})
}

func (c *connState) restartContainer(env *protocol.Envelope) {
	var req protocol.RestartContainerReq
	if err := protocol.DecodeBody(env.Body, &req); err != nil {
		c.sendError(env.ID, "invalid body")
		return
	}
	if err := c.ss.docker.RestartContainer(context.Background(), req.ContainerID); err != nil {
		c.sendError(env.ID, fmt.Sprintf("restart: %v", err))
		return
	}
	c.sendResult(env.ID, &protocol.Result{OK: true, Message: "restarted"})
}

// --- Converters: agent types -> protocol types ---

func convertTimedHost(src []TimedHostMetrics) []protocol.TimedHostMetrics {
	out := make([]protocol.TimedHostMetrics, len(src))
	for i, s := range src {
		out[i] = protocol.TimedHostMetrics{
			Timestamp: s.Timestamp.Unix(),
			HostMetrics: protocol.HostMetrics{
				CPUPercent: s.CPUPercent, MemTotal: s.MemTotal, MemUsed: s.MemUsed, MemPercent: s.MemPercent,
				SwapTotal: s.SwapTotal, SwapUsed: s.SwapUsed,
				Load1: s.Load1, Load5: s.Load5, Load15: s.Load15, Uptime: s.Uptime,
			},
		}
	}
	return out
}

func convertTimedDisk(src []TimedDiskMetrics) []protocol.TimedDiskMetrics {
	out := make([]protocol.TimedDiskMetrics, len(src))
	for i, s := range src {
		out[i] = protocol.TimedDiskMetrics{
			Timestamp: s.Timestamp.Unix(),
			DiskMetrics: protocol.DiskMetrics{
				Mountpoint: s.Mountpoint, Device: s.Device,
				Total: s.Total, Used: s.Used, Free: s.Free, Percent: s.Percent,
			},
		}
	}
	return out
}

func convertTimedNet(src []TimedNetMetrics) []protocol.TimedNetMetrics {
	out := make([]protocol.TimedNetMetrics, len(src))
	for i, s := range src {
		out[i] = protocol.TimedNetMetrics{
			Timestamp: s.Timestamp.Unix(),
			NetMetrics: protocol.NetMetrics{
				Iface: s.Iface, RxBytes: s.RxBytes, TxBytes: s.TxBytes,
				RxPackets: s.RxPackets, TxPackets: s.TxPackets,
				RxErrors: s.RxErrors, TxErrors: s.TxErrors,
			},
		}
	}
	return out
}

func convertTimedContainer(src []TimedContainerMetrics) []protocol.TimedContainerMetrics {
	out := make([]protocol.TimedContainerMetrics, len(src))
	for i, s := range src {
		out[i] = protocol.TimedContainerMetrics{
			Timestamp: s.Timestamp.Unix(),
			ContainerMetrics: protocol.ContainerMetrics{
				ID: s.ID, Name: s.Name, Image: s.Image, State: s.State,
				CPUPercent: s.CPUPercent, MemUsage: s.MemUsage, MemLimit: s.MemLimit, MemPercent: s.MemPercent,
				NetRx: s.NetRx, NetTx: s.NetTx, BlockRead: s.BlockRead, BlockWrite: s.BlockWrite, PIDs: s.PIDs,
			},
		}
	}
	return out
}

func convertLogEntries(src []LogEntry) []protocol.LogEntryMsg {
	out := make([]protocol.LogEntryMsg, len(src))
	for i, s := range src {
		out[i] = protocol.LogEntryMsg{
			Timestamp:     s.Timestamp.Unix(),
			ContainerID:   s.ContainerID,
			ContainerName: s.ContainerName,
			Stream:        s.Stream,
			Message:       s.Message,
		}
	}
	return out
}

func convertAlerts(src []Alert) []protocol.AlertMsg {
	out := make([]protocol.AlertMsg, len(src))
	for i, s := range src {
		out[i] = protocol.AlertMsg{
			ID:           s.ID,
			RuleName:     s.RuleName,
			Severity:     s.Severity,
			Condition:    s.Condition,
			InstanceKey:  s.InstanceKey,
			FiredAt:      s.FiredAt.Unix(),
			Message:      s.Message,
			Acknowledged: s.Acknowledged,
		}
		if s.ResolvedAt != nil {
			out[i].ResolvedAt = s.ResolvedAt.Unix()
		}
	}
	return out
}

func isClosedErr(err error) bool {
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	// net.ErrClosed is not always returned (e.g., on old Go versions).
	return strings.Contains(err.Error(), "use of closed network connection")
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
