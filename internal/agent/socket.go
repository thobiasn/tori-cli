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

const maxConnections = 64

// defaultMaxQueryRange is the fallback max query range (24 hours) when no retention is set.
const defaultMaxQueryRange = 24 * 60 * 60

// maxSilenceDuration is the maximum silence duration (30 days in seconds).
const maxSilenceDuration = 30 * 24 * 60 * 60

// SocketServer serves protocol messages over a Unix domain socket.
type SocketServer struct {
	hub           *Hub
	store         *Store
	docker        *DockerCollector
	retentionDays int
	listener      net.Listener
	path          string
	wg            sync.WaitGroup
	connSem       chan struct{}

	alerterMu sync.RWMutex
	alerter   *Alerter
}

// NewSocketServer creates a SocketServer. Call Start to begin accepting connections.
// retentionDays controls the maximum query range; 0 falls back to 24h.
func NewSocketServer(hub *Hub, store *Store, docker *DockerCollector, alerter *Alerter, retentionDays int) *SocketServer {
	return &SocketServer{
		hub:           hub,
		store:         store,
		docker:        docker,
		alerter:       alerter,
		retentionDays: retentionDays,
		connSem:       make(chan struct{}, maxConnections),
	}
}

// SetAlerter replaces the alerter used for silence operations.
func (ss *SocketServer) SetAlerter(a *Alerter) {
	ss.alerterMu.Lock()
	defer ss.alerterMu.Unlock()
	ss.alerter = a
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

	// World-accessible: SSH is the auth gate, not file permissions.
	if err := os.Chmod(path, 0666); err != nil {
		ln.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	ss.listener = ln
	ss.path = path
	ss.wg.Add(1)
	go ss.acceptLoop()
	slog.Info("socket server started", "path", path)
	return nil
}

// Stop closes the listener, waits for all connections, and removes the socket file.
func (ss *SocketServer) Stop() {
	if ss.listener != nil {
		ss.listener.Close()
	}
	ss.wg.Wait()
	if ss.path != "" {
		os.Remove(ss.path)
	}
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

		// Enforce connection limit.
		select {
		case ss.connSem <- struct{}{}:
		default:
			slog.Warn("connection limit reached, rejecting")
			conn.Close()
			continue
		}

		ss.wg.Add(1)
		go ss.handleConn(conn)
	}
}

func (ss *SocketServer) handleConn(conn net.Conn) {
	defer ss.wg.Done()
	defer conn.Close()
	defer func() { <-ss.connSem }()

	slog.Info("client connected", "remote", conn.RemoteAddr())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := &connState{
		ss:   ss,
		conn: conn,
		ctx:  ctx,
		subs: make(map[string]*subscription),
	}
	defer c.cleanup()
	defer slog.Info("client disconnected", "remote", conn.RemoteAddr())

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
	ctx     context.Context // cancelled when connection closes
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

func (c *connState) sendResponse(id uint32, body any) {
	env, err := protocol.NewEnvelope(protocol.TypeResult, id, body)
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
		c.subscribeMetrics()
	case protocol.TypeSubscribeLogs:
		c.subscribeLogs(env)
	case protocol.TypeSubscribeAlerts:
		c.subscribeAlerts()
	case protocol.TypeSubscribeContainers:
		c.subscribeContainers()
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
	case protocol.TypeActionSetTracking:
		c.setTracking(env)
	case protocol.TypeQueryTracking:
		c.queryTracking(env)

	default:
		c.sendError(env.ID, fmt.Sprintf("unknown message type: %s", env.Type))
	}
}

// --- Streaming ---

func (c *connState) subscribeMetrics() {
	c.subscribeSimple(TopicMetrics, protocol.TypeMetricsUpdate)
}

func (c *connState) subscribeLogs(env *protocol.Envelope) {
	if _, exists := c.subs[TopicLogs]; exists {
		return
	}

	var filter protocol.SubscribeLogs
	if env.Body != nil {
		if err := protocol.DecodeBody(env.Body, &filter); err != nil {
			c.sendError(env.ID, "invalid subscribe body")
			return
		}
	}

	sub, ch := c.ss.hub.Subscribe(TopicLogs)
	ctx, cancel := context.WithCancel(c.ctx)
	c.subs[TopicLogs] = &subscription{sub: sub, topic: TopicLogs, cancel: cancel}

	// Capture project name for dynamic resolution (not a snapshot of IDs).
	project := filter.Project

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
				if project != "" && !c.containerInProject(entry.ContainerID, project) {
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

// containerInProject checks if a container belongs to a project using the live container list.
func (c *connState) containerInProject(containerID, project string) bool {
	for _, ctr := range c.ss.docker.Containers() {
		if ctr.ID == containerID && ctr.Project == project {
			return true
		}
	}
	return false
}

func (c *connState) subscribeAlerts() {
	c.subscribeSimple(TopicAlerts, protocol.TypeAlertEvent)
}

func (c *connState) subscribeContainers() {
	c.subscribeSimple(TopicContainers, protocol.TypeContainerEvent)
}

// subscribeSimple sets up a streaming subscription that forwards all messages
// on the given topic to the client as envelopes of the given type.
func (c *connState) subscribeSimple(topic string, envType protocol.MsgType) {
	if _, exists := c.subs[topic]; exists {
		return
	}

	sub, ch := c.ss.hub.Subscribe(topic)
	ctx, cancel := context.WithCancel(c.ctx)
	c.subs[topic] = &subscription{sub: sub, topic: topic, cancel: cancel}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				env, err := protocol.NewEnvelope(envType, 0, msg)
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
	if req.Start > req.End {
		c.sendError(env.ID, "start must be <= end")
		return
	}
	maxRange := int64(c.ss.retentionDays) * 86400
	if maxRange <= 0 {
		maxRange = defaultMaxQueryRange
	}
	if req.End-req.Start > maxRange {
		c.sendError(env.ID, fmt.Sprintf("time range too large (max %dd)", c.ss.retentionDays))
		return
	}

	host, err := c.ss.store.QueryHostMetrics(c.ctx, req.Start, req.End)
	if err != nil {
		slog.Error("query host metrics", "error", err)
		c.sendError(env.ID, "query failed")
		return
	}
	disks, err := c.ss.store.QueryDiskMetrics(c.ctx, req.Start, req.End)
	if err != nil {
		slog.Error("query disk metrics", "error", err)
		c.sendError(env.ID, "query failed")
		return
	}
	nets, err := c.ss.store.QueryNetMetrics(c.ctx, req.Start, req.End)
	if err != nil {
		slog.Error("query net metrics", "error", err)
		c.sendError(env.ID, "query failed")
		return
	}
	containers, err := c.ss.store.QueryContainerMetrics(c.ctx, req.Start, req.End)
	if err != nil {
		slog.Error("query container metrics", "error", err)
		c.sendError(env.ID, "query failed")
		return
	}

	hostOut := convertTimedHost(host)
	containerOut := convertTimedContainer(containers)
	if req.Points > 0 {
		hostOut = downsampleHost(hostOut, req.Points)
		containerOut = downsampleContainers(containerOut, req.Points)
	}

	resp := protocol.QueryMetricsResp{
		Host:          hostOut,
		Disks:         convertTimedDisk(disks),
		Networks:      convertTimedNet(nets),
		Containers:    containerOut,
		RetentionDays: c.ss.retentionDays,
	}
	c.sendResponse(env.ID, &resp)
}

func (c *connState) queryLogs(env *protocol.Envelope) {
	var req protocol.QueryLogsReq
	if err := protocol.DecodeBody(env.Body, &req); err != nil {
		c.sendError(env.ID, "invalid query body")
		return
	}
	if req.Start > req.End {
		c.sendError(env.ID, "start must be <= end")
		return
	}

	filter := LogFilter{
		Start:  req.Start,
		End:    req.End,
		Stream: req.Stream,
		Search: req.Search,
		Limit:  req.Limit,
	}
	if len(req.ContainerIDs) > 0 {
		filter.ContainerIDs = req.ContainerIDs
	} else if req.ContainerID != "" {
		filter.ContainerIDs = []string{req.ContainerID}
	}

	entries, err := c.ss.store.QueryLogs(c.ctx, filter)
	if err != nil {
		slog.Error("query logs", "error", err)
		c.sendError(env.ID, "query failed")
		return
	}

	resp := protocol.QueryLogsResp{Entries: convertLogEntries(entries)}
	c.sendResponse(env.ID, &resp)
}

func (c *connState) queryAlerts(env *protocol.Envelope) {
	var req protocol.QueryAlertsReq
	if err := protocol.DecodeBody(env.Body, &req); err != nil {
		c.sendError(env.ID, "invalid query body")
		return
	}
	if req.Start > req.End {
		c.sendError(env.ID, "start must be <= end")
		return
	}

	alerts, err := c.ss.store.QueryAlerts(c.ctx, req.Start, req.End)
	if err != nil {
		slog.Error("query alerts", "error", err)
		c.sendError(env.ID, "query failed")
		return
	}

	resp := protocol.QueryAlertsResp{Alerts: convertAlerts(alerts)}
	c.sendResponse(env.ID, &resp)
}

func (c *connState) queryContainers(id uint32) {
	containers := c.ss.docker.Containers()
	resp := protocol.QueryContainersResp{
		Containers: make([]protocol.ContainerInfo, len(containers)),
	}
	for i, ctr := range containers {
		resp.Containers[i] = protocol.ContainerInfo{
			ID:           ctr.ID,
			Name:         ctr.Name,
			Image:        ctr.Image,
			State:        ctr.State,
			Project:      ctr.Project,
			Health:       ctr.Health,
			StartedAt:    ctr.StartedAt,
			RestartCount: ctr.RestartCount,
			ExitCode:     ctr.ExitCode,
			Tracked:      c.ss.docker.IsTracked(ctr.Name, ctr.Project),
		}
	}
	c.sendResponse(id, &resp)
}

// --- Actions ---

func (c *connState) ackAlert(env *protocol.Envelope) {
	var req protocol.AckAlertReq
	if err := protocol.DecodeBody(env.Body, &req); err != nil {
		c.sendError(env.ID, "invalid body")
		return
	}
	if err := c.ss.store.AckAlert(c.ctx, req.AlertID); err != nil {
		c.sendError(env.ID, "alert not found")
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
	c.ss.alerterMu.RLock()
	alerter := c.ss.alerter
	c.ss.alerterMu.RUnlock()
	if alerter == nil {
		c.sendError(env.ID, "alerter not configured")
		return
	}
	if req.Duration <= 0 || req.Duration > maxSilenceDuration {
		c.sendError(env.ID, fmt.Sprintf("duration must be 1-%d seconds", maxSilenceDuration))
		return
	}
	if !alerter.HasRule(req.RuleName) {
		c.sendError(env.ID, "unknown rule name")
		return
	}
	alerter.Silence(req.RuleName, time.Duration(req.Duration)*time.Second)
	c.sendResult(env.ID, &protocol.Result{OK: true, Message: "silenced"})
}

func (c *connState) setTracking(env *protocol.Envelope) {
	var req protocol.SetTrackingReq
	if err := protocol.DecodeBody(env.Body, &req); err != nil {
		c.sendError(env.ID, "invalid body")
		return
	}
	if (req.Container == "") == (req.Project == "") {
		c.sendError(env.ID, "exactly one of container or project must be set")
		return
	}
	name := truncate(req.Container, maxNameLen)
	project := truncate(req.Project, maxLabelLen)
	c.ss.docker.SetTracking(name, project, req.Tracked)
	c.sendResult(env.ID, &protocol.Result{OK: true, Message: "tracking updated"})
}

func (c *connState) queryTracking(env *protocol.Envelope) {
	containers, projects := c.ss.docker.GetTrackingState()
	resp := protocol.QueryTrackingResp{
		UntrackedContainers: containers,
		UntrackedProjects:   projects,
	}
	if resp.UntrackedContainers == nil {
		resp.UntrackedContainers = []string{}
	}
	if resp.UntrackedProjects == nil {
		resp.UntrackedProjects = []string{}
	}
	c.sendResponse(env.ID, &resp)
}

func (c *connState) restartContainer(env *protocol.Envelope) {
	var req protocol.RestartContainerReq
	if err := protocol.DecodeBody(env.Body, &req); err != nil {
		c.sendError(env.ID, "invalid body")
		return
	}
	// Validate container is being monitored.
	if !c.isMonitoredContainer(req.ContainerID) {
		c.sendError(env.ID, "container not found")
		return
	}
	if err := c.ss.docker.RestartContainer(c.ctx, req.ContainerID); err != nil {
		slog.Error("restart container", "container", req.ContainerID, "error", err)
		c.sendError(env.ID, "restart failed")
		return
	}
	c.sendResult(env.ID, &protocol.Result{OK: true, Message: "restarted"})
}

func (c *connState) isMonitoredContainer(id string) bool {
	for _, ctr := range c.ss.docker.Containers() {
		if ctr.ID == id {
			return true
		}
	}
	return false
}

// --- Downsampling ---

// downsampleHost reduces a host metric slice to at most n points using
// max-per-bucket aggregation, preserving spikes.
func downsampleHost(data []protocol.TimedHostMetrics, n int) []protocol.TimedHostMetrics {
	if len(data) <= n || n <= 0 {
		return data
	}
	out := make([]protocol.TimedHostMetrics, 0, n)
	bucketSize := float64(len(data)) / float64(n)
	for i := 0; i < n; i++ {
		lo := int(float64(i) * bucketSize)
		hi := int(float64(i+1) * bucketSize)
		if hi > len(data) {
			hi = len(data)
		}
		if lo >= hi {
			continue
		}
		best := data[lo]
		for j := lo + 1; j < hi; j++ {
			d := data[j]
			if d.CPUPercent > best.CPUPercent {
				best.CPUPercent = d.CPUPercent
			}
			if d.MemPercent > best.MemPercent {
				best.MemPercent = d.MemPercent
			}
			if d.MemUsed > best.MemUsed {
				best.MemUsed = d.MemUsed
			}
			if d.Load1 > best.Load1 {
				best.Load1 = d.Load1
			}
			if d.Load5 > best.Load5 {
				best.Load5 = d.Load5
			}
			if d.Load15 > best.Load15 {
				best.Load15 = d.Load15
			}
		}
		// Keep the last timestamp in the bucket for monotonicity.
		best.Timestamp = data[hi-1].Timestamp
		out = append(out, best)
	}
	return out
}

// downsampleContainers reduces container metrics to at most n points per
// container using max-per-bucket aggregation.
func downsampleContainers(data []protocol.TimedContainerMetrics, n int) []protocol.TimedContainerMetrics {
	if n <= 0 {
		return data
	}
	// Group by container ID.
	byID := make(map[string][]protocol.TimedContainerMetrics)
	var order []string
	for _, m := range data {
		if _, seen := byID[m.ID]; !seen {
			order = append(order, m.ID)
		}
		byID[m.ID] = append(byID[m.ID], m)
	}
	var out []protocol.TimedContainerMetrics
	for _, id := range order {
		series := byID[id]
		if len(series) <= n {
			out = append(out, series...)
			continue
		}
		bucketSize := float64(len(series)) / float64(n)
		for i := 0; i < n; i++ {
			lo := int(float64(i) * bucketSize)
			hi := int(float64(i+1) * bucketSize)
			if hi > len(series) {
				hi = len(series)
			}
			if lo >= hi {
				continue
			}
			best := series[lo]
			for j := lo + 1; j < hi; j++ {
				d := series[j]
				if d.CPUPercent > best.CPUPercent {
					best.CPUPercent = d.CPUPercent
				}
				if d.MemUsage > best.MemUsage {
					best.MemUsage = d.MemUsage
				}
				if d.MemPercent > best.MemPercent {
					best.MemPercent = d.MemPercent
				}
			}
			best.Timestamp = series[hi-1].Timestamp
			out = append(out, best)
		}
	}
	return out
}

// --- Converters: agent types -> protocol types ---

func convertTimedHost(src []TimedHostMetrics) []protocol.TimedHostMetrics {
	out := make([]protocol.TimedHostMetrics, len(src))
	for i, s := range src {
		out[i] = protocol.TimedHostMetrics{
			Timestamp: s.Timestamp.Unix(),
			HostMetrics: protocol.HostMetrics{
				CPUPercent: s.CPUPercent, MemTotal: s.MemTotal, MemUsed: s.MemUsed, MemPercent: s.MemPercent,
				MemCached: s.MemCached, MemFree: s.MemFree,
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
				Health: s.Health, StartedAt: s.StartedAt, RestartCount: s.RestartCount, ExitCode: s.ExitCode,
				CPUPercent: s.CPUPercent, MemUsage: s.MemUsage, MemLimit: s.MemLimit, MemPercent: s.MemPercent,
				NetRx: s.NetRx, NetTx: s.NetTx, BlockRead: s.BlockRead, BlockWrite: s.BlockWrite, PIDs: s.PIDs,
				DiskUsage: s.DiskUsage,
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
	return strings.Contains(err.Error(), "use of closed network connection")
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
