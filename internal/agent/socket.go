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

	"github.com/thobiasn/tori-cli/internal/protocol"
)

const maxConnections = 64

// defaultMaxQueryRange is the fallback max query range (24 hours) when no retention is set.
const defaultMaxQueryRange = 24 * 60 * 60

// maxSilenceDuration is the maximum silence duration (30 days in seconds).
const maxSilenceDuration = 30 * 24 * 60 * 60

// maxDownsamplePoints caps the Points parameter in downsampled queries.
const maxDownsamplePoints = 4096

// maxLogLimit caps the Limit parameter in log queries.
const maxLogLimit = 10000

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

// maxQueryRange returns the maximum allowed query range in seconds.
func (ss *SocketServer) maxQueryRange() int64 {
	r := int64(ss.retentionDays) * 86400
	if r <= 0 {
		return defaultMaxQueryRange
	}
	return r
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

// checkTimeRange validates start <= end and range <= maxQueryRange.
// Returns true if valid; sends an error response and returns false otherwise.
func (c *connState) checkTimeRange(id uint32, start, end int64) bool {
	if start > end {
		c.sendError(id, "start must be <= end")
		return false
	}
	maxRange := c.ss.maxQueryRange()
	if end-start > maxRange {
		days := maxRange / 86400
		if days <= 0 {
			days = 1
		}
		c.sendError(id, fmt.Sprintf("time range too large (max %dd)", days))
		return false
	}
	return true
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

	// Send current firing alerts so reconnecting clients see existing state.
	alerts, err := c.ss.store.QueryFiringAlerts(c.ctx)
	if err != nil {
		slog.Warn("query firing alerts for snapshot", "error", err)
		return
	}
	for _, a := range alerts {
		event := &protocol.AlertEvent{
			ID:          a.ID,
			RuleName:    a.RuleName,
			Severity:    a.Severity,
			Condition:   a.Condition,
			InstanceKey: a.InstanceKey,
			FiredAt:     a.FiredAt.Unix(),
			Message:     a.Message,
			State:       "firing",
		}
		env, err := protocol.NewEnvelope(protocol.TypeAlertEvent, 0, event)
		if err != nil {
			continue
		}
		c.writeMsg(env)
	}
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
	if !c.checkTimeRange(env.ID, req.Start, req.End) {
		return
	}

	// Cap downsampling points to prevent OOM from oversized allocations.
	if req.Points > maxDownsamplePoints {
		req.Points = maxDownsamplePoints
	}

	host, err := c.ss.store.QueryHostMetrics(c.ctx, req.Start, req.End)
	if err != nil {
		slog.Error("query host metrics", "error", err)
		c.sendError(env.ID, "query failed")
		return
	}
	var cmFilter []ContainerMetricsFilter
	if req.Service != "" || req.Project != "" {
		cmFilter = append(cmFilter, ContainerMetricsFilter{
			Project: truncate(req.Project, maxLabelLen),
			Service: truncate(req.Service, maxLabelLen),
		})
	}
	containers, err := c.ss.store.QueryContainerMetrics(c.ctx, req.Start, req.End, cmFilter...)
	if err != nil {
		slog.Error("query container metrics", "error", err)
		c.sendError(env.ID, "query failed")
		return
	}

	hostOut := convertTimedHost(host)
	containerOut := convertTimedContainer(containers)

	resp := protocol.QueryMetricsResp{
		RetentionDays: c.ss.retentionDays,
	}

	if req.Points > 0 {
		// Downsampled backfill: TUI only uses Host and Containers.
		// Skip disk/net queries to keep response size reasonable on wide windows.
		resp.Host = downsampleHost(hostOut, req.Points, req.Start, req.End)
		resp.Containers = downsampleContainers(containerOut, req.Points, req.Start, req.End)
	} else {
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
		resp.Host = hostOut
		resp.Disks = convertTimedDisk(disks)
		resp.Networks = convertTimedNet(nets)
		resp.Containers = containerOut
	}
	c.sendResponse(env.ID, &resp)
}

func (c *connState) queryLogs(env *protocol.Envelope) {
	var req protocol.QueryLogsReq
	if err := protocol.DecodeBody(env.Body, &req); err != nil {
		c.sendError(env.ID, "invalid query body")
		return
	}
	if !c.checkTimeRange(env.ID, req.Start, req.End) {
		return
	}

	// Cap log limit to prevent oversized result sets.
	if req.Limit > maxLogLimit {
		req.Limit = maxLogLimit
	}

	filter := LogFilter{
		Start:   req.Start,
		End:     req.End,
		Project: truncate(req.Project, maxLabelLen),
		Service: truncate(req.Service, maxLabelLen),
		Stream:  req.Stream,
		Search:  req.Search,
		Limit:   req.Limit,
	}
	if filter.Service == "" {
		if len(req.ContainerIDs) > 0 {
			filter.ContainerIDs = req.ContainerIDs
		} else if req.ContainerID != "" {
			filter.ContainerIDs = []string{req.ContainerID}
		}
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
	if !c.checkTimeRange(env.ID, req.Start, req.End) {
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
			Service:      ctr.Service,
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

	// Persist tracking state. Best-effort — log error but don't fail the request.
	containers, projects := c.ss.docker.GetTrackingState()
	if err := c.ss.store.SaveTracking(c.ctx, containers, projects); err != nil {
		slog.Warn("failed to persist tracking state", "error", err)
	}

	c.sendResult(env.ID, &protocol.Result{OK: true, Message: "tracking updated"})
}

func (c *connState) queryTracking(env *protocol.Envelope) {
	containers, projects := c.ss.docker.GetTrackingState()
	resp := protocol.QueryTrackingResp{
		TrackedContainers: containers,
		TrackedProjects:   projects,
	}
	if resp.TrackedContainers == nil {
		resp.TrackedContainers = []string{}
	}
	if resp.TrackedProjects == nil {
		resp.TrackedProjects = []string{}
	}
	c.sendResponse(env.ID, &resp)
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
