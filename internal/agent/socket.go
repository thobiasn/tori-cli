package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
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

// maxSearchLen caps the Search string in log queries and subscriptions.
const maxSearchLen = 512

// SocketServer serves protocol messages over a Unix domain socket.
type SocketServer struct {
	hub           *Hub
	store         *Store
	docker        *DockerCollector
	version       string
	retentionDays atomic.Int32
	listener      net.Listener
	path          string
	wg            sync.WaitGroup
	connSem       chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc

	alerterMu      sync.RWMutex
	alerter        *Alerter
	lastTestNotify atomic.Int64 // unix timestamp of last test notification
}

// NewSocketServer creates a SocketServer. Call Start to begin accepting connections.
// retentionDays controls the maximum query range; 0 falls back to 24h.
func NewSocketServer(hub *Hub, store *Store, docker *DockerCollector, alerter *Alerter, retentionDays int, version string) *SocketServer {
	ss := &SocketServer{
		hub:     hub,
		store:   store,
		docker:  docker,
		alerter: alerter,
		version: version,
		connSem: make(chan struct{}, maxConnections),
	}
	ss.retentionDays.Store(int32(retentionDays))
	return ss
}

// SetAlerter replaces the alerter used for silence operations.
func (ss *SocketServer) SetAlerter(a *Alerter) {
	ss.alerterMu.Lock()
	defer ss.alerterMu.Unlock()
	ss.alerter = a
}

// SetRetentionDays updates the retention days used for query range limits.
func (ss *SocketServer) SetRetentionDays(days int) {
	ss.retentionDays.Store(int32(days))
}

// maxQueryRange returns the maximum allowed query range in seconds.
func (ss *SocketServer) maxQueryRange() int64 {
	r := int64(ss.retentionDays.Load()) * 86400
	if r <= 0 {
		return defaultMaxQueryRange
	}
	return r
}

// Start begins listening on the given Unix socket path with the specified file mode.
func (ss *SocketServer) Start(path string, mode fs.FileMode) error {
	// Remove stale socket file.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	if err := os.Chmod(path, mode); err != nil {
		ln.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	ss.ctx, ss.cancel = context.WithCancel(context.Background())
	ss.listener = ln
	ss.path = path
	ss.wg.Add(1)
	go ss.acceptLoop()
	slog.Info("socket server started", "path", path)
	return nil
}

// Stop closes the listener, waits for all connections, and removes the socket file.
func (ss *SocketServer) Stop() {
	if ss.cancel != nil {
		ss.cancel()
	}
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

	ctx, cancel := context.WithCancel(ss.ctx)
	defer cancel()

	// Close the connection when context is cancelled so the blocking
	// ReadMsg call below unblocks during shutdown.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

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
			if !isEOF(err) && !isClosedErr(err) && ctx.Err() == nil {
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

	// Hello.
	case protocol.TypeHello:
		c.hello(env)

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
	case protocol.TypeActionTestNotify:
		c.testNotify(env)
	case protocol.TypeQueryTracking:
		c.queryTracking(env)
	case protocol.TypeQueryAlertRules:
		c.queryAlertRules(env.ID)

	default:
		c.sendError(env.ID, fmt.Sprintf("unknown message type: %s", env.Type))
	}
}

// --- Hello ---

func (c *connState) hello(env *protocol.Envelope) {
	c.sendResponse(env.ID, &protocol.HelloResp{
		ProtocolVersion: protocol.ProtocolVersion,
		Version:         c.ss.version,
	})
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

	if !validLogLevel(filter.Level) {
		filter.Level = ""
	}

	// Compile search regex once before the goroutine.
	filter.Search = truncate(filter.Search, maxSearchLen)
	var searchRe *regexp.Regexp
	if filter.Search != "" {
		re, err := regexp.Compile("(?i)" + filter.Search)
		if err != nil {
			re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(filter.Search))
		}
		searchRe = re
	}

	sub, ch := c.ss.hub.Subscribe(TopicLogs)
	ctx, cancel := context.WithCancel(c.ctx)
	c.subs[TopicLogs] = &subscription{sub: sub, topic: TopicLogs, cancel: cancel}

	// Capture project name for dynamic resolution (not a snapshot of IDs).
	project := filter.Project
	level := filter.Level

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
				if project != "" && c.ss.docker.ContainerProject(entry.ContainerID) != project {
					continue
				}
				if level != "" && entry.Level != level {
					continue
				}
				if searchRe != nil && !searchRe.MatchString(entry.Message) {
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
			ID:           a.ID,
			RuleName:     a.RuleName,
			Severity:     a.Severity,
			Condition:    a.Condition,
			InstanceKey:  a.InstanceKey,
			FiredAt:      a.FiredAt.Unix(),
			Message:      a.Message,
			State:        "firing",
			Acknowledged: a.Acknowledged,
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

	var cmFilter []ContainerMetricsFilter
	if req.Service != "" || req.Project != "" {
		cmFilter = append(cmFilter, ContainerMetricsFilter{
			Project: truncate(req.Project, maxLabelLen),
			Service: truncate(req.Service, maxLabelLen),
		})
	}

	resp := protocol.QueryMetricsResp{
		RetentionDays: int(c.ss.retentionDays.Load()),
	}

	if req.Points > 0 {
		// Downsampled backfill: aggregate in SQL to avoid loading all raw data.
		bucketDur := (req.End - req.Start) / int64(req.Points)
		if bucketDur <= 0 {
			bucketDur = 1
		}
		host, err := c.ss.store.QueryHostMetricsGrouped(c.ctx, req.Start, req.End, bucketDur)
		if err != nil {
			slog.Error("query host metrics", "error", err)
			c.sendError(env.ID, "query failed")
			return
		}
		containers, err := c.ss.store.QueryContainerMetricsGrouped(c.ctx, req.Start, req.End, bucketDur, cmFilter...)
		if err != nil {
			slog.Error("query container metrics", "error", err)
			c.sendError(env.ID, "query failed")
			return
		}
		resp.Host = downsampleHost(convertTimedHost(host), req.Points, req.Start, req.End)
		resp.Containers = downsampleContainers(convertTimedContainer(containers), req.Points, req.Start, req.End)
	} else {
		host, err := c.ss.store.QueryHostMetrics(c.ctx, req.Start, req.End)
		if err != nil {
			slog.Error("query host metrics", "error", err)
			c.sendError(env.ID, "query failed")
			return
		}
		containers, err := c.ss.store.QueryContainerMetrics(c.ctx, req.Start, req.End, cmFilter...)
		if err != nil {
			slog.Error("query container metrics", "error", err)
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
		resp.Host = convertTimedHost(host)
		resp.Disks = convertTimedDisk(disks)
		resp.Networks = convertTimedNet(nets)
		resp.Containers = convertTimedContainer(containers)
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
	if !validLogLevel(req.Level) {
		req.Level = ""
	}

	search := truncate(req.Search, maxSearchLen)
	searchIsRegex := false
	if search != "" {
		if _, err := regexp.Compile(search); err == nil {
			searchIsRegex = true
		}
	}

	filter := LogFilter{
		Start:         req.Start,
		End:           req.End,
		Project:       truncate(req.Project, maxLabelLen),
		Service:       truncate(req.Service, maxLabelLen),
		Search:        search,
		SearchIsRegex: searchIsRegex,
		Level:         req.Level,
		Limit:         req.Limit,
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

	if req.SkipCount {
		resp.Total = -1
	} else {
		// Count total logs in scope (excluding search/stream/limit) so the TUI
		// can show "X of Y". Non-fatal — proceed with 0 if it fails.
		countFilter := LogFilter{
			Start:        filter.Start,
			End:          filter.End,
			ContainerIDs: filter.ContainerIDs,
			Project:      filter.Project,
			Service:      filter.Service,
		}
		if total, err := c.ss.store.CountLogs(c.ctx, countFilter); err != nil {
			slog.Warn("count logs", "error", err)
		} else {
			resp.Total = total
		}
	}

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
			Tracked:      c.ss.docker.IsTracked(ctr.Name),
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
	if req.Duration < 0 || req.Duration > maxSilenceDuration {
		c.sendError(env.ID, fmt.Sprintf("duration must be 0-%d seconds", maxSilenceDuration))
		return
	}
	if !alerter.HasRule(req.RuleName) {
		c.sendError(env.ID, "unknown rule name")
		return
	}
	alerter.Silence(req.RuleName, time.Duration(req.Duration)*time.Second)
	msg := "silenced"
	if req.Duration == 0 {
		msg = "unsilenced"
	}
	c.sendResult(env.ID, &protocol.Result{OK: true, Message: msg})
}

func (c *connState) testNotify(env *protocol.Envelope) {
	var req protocol.TestNotifyReq
	if err := protocol.DecodeBody(env.Body, &req); err != nil {
		c.sendError(env.ID, "invalid body")
		return
	}
	if len(req.RuleName) == 0 || len(req.RuleName) > maxNameLen {
		c.sendError(env.ID, "invalid rule name")
		return
	}

	c.ss.alerterMu.RLock()
	alerter := c.ss.alerter
	c.ss.alerterMu.RUnlock()
	if alerter == nil {
		c.sendError(env.ID, "alerter not configured")
		return
	}
	if !alerter.HasRule(req.RuleName) {
		c.sendError(env.ID, "unknown rule name")
		return
	}

	// Rate limit: one test notification per 60 seconds (CAS to avoid TOCTOU).
	for {
		last := c.ss.lastTestNotify.Load()
		now := time.Now().Unix()
		elapsed := now - last
		if elapsed < 60 {
			c.sendError(env.ID, fmt.Sprintf("rate limited, try again in %ds", 60-elapsed))
			return
		}
		if c.ss.lastTestNotify.CompareAndSwap(last, now) {
			break
		}
	}

	if err := alerter.SendTestNotification(req.RuleName); err != nil {
		c.sendError(env.ID, err.Error())
		return
	}
	c.sendResult(env.ID, &protocol.Result{OK: true, Message: "test notification sent"})
}

func (c *connState) setTracking(env *protocol.Envelope) {
	var req protocol.SetTrackingReq
	if err := protocol.DecodeBody(env.Body, &req); err != nil {
		c.sendError(env.ID, "invalid body")
		return
	}
	if req.Container == "" && req.Project == "" {
		c.sendError(env.ID, "container or project must be set")
		return
	}
	name := truncate(req.Container, maxNameLen)
	project := truncate(req.Project, maxLabelLen)
	c.ss.docker.SetTracking(name, project, req.Tracked)

	// Persist tracking state. Best-effort — log error but don't fail the request.
	containers := c.ss.docker.GetTrackingState()
	if err := c.ss.store.SaveTracking(c.ctx, containers); err != nil {
		slog.Warn("failed to persist tracking state", "error", err)
	}

	c.sendResult(env.ID, &protocol.Result{OK: true, Message: "tracking updated"})
}

func (c *connState) queryTracking(env *protocol.Envelope) {
	containers := c.ss.docker.GetTrackingState()
	resp := protocol.QueryTrackingResp{
		TrackedContainers: containers,
		TrackedProjects:   []string{}, // no longer used, kept for wire compat
	}
	if resp.TrackedContainers == nil {
		resp.TrackedContainers = []string{}
	}
	c.sendResponse(env.ID, &resp)
}

func (c *connState) queryAlertRules(id uint32) {
	c.ss.alerterMu.RLock()
	alerter := c.ss.alerter
	c.ss.alerterMu.RUnlock()

	var rules []protocol.AlertRuleInfo
	if alerter != nil {
		for _, rs := range alerter.QueryRules() {
			info := protocol.AlertRuleInfo{
				Name:        rs.Name,
				Condition:   rs.Condition,
				Severity:    rs.Severity,
				Actions:     rs.Actions,
				FiringCount: rs.FiringCount,
			}
			if rs.For > 0 {
				info.For = rs.For.String()
			}
			if rs.Cooldown > 0 {
				info.Cooldown = rs.Cooldown.String()
			}
			if rs.NotifyCooldown > 0 {
				info.NotifyCooldown = rs.NotifyCooldown.String()
			}
			if !rs.SilencedUntil.IsZero() {
				info.SilencedUntil = rs.SilencedUntil.Unix()
			}
			if rs.Match != "" {
				info.Match = rs.Match
				info.MatchRegex = rs.MatchRegex
			}
			if rs.Window > 0 {
				info.Window = rs.Window.String()
			}
			rules = append(rules, info)
		}
	}
	if rules == nil {
		rules = []protocol.AlertRuleInfo{}
	}
	c.sendResponse(id, &protocol.QueryAlertRulesResp{Rules: rules})
}

// validLogLevel returns true if the level is a recognized log level or empty.
func validLogLevel(level string) bool {
	switch level {
	case "", "ERR", "WARN", "INFO", "DBUG":
		return true
	}
	return false
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
