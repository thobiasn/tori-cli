package tui2

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/thobiasn/tori-cli/internal/protocol"
)

// appCtx is shared mutable state between the bubbletea copy of App and
// the connect goroutines.
type appCtx struct {
	prog *tea.Program
}

// sshPromptState holds the state of an active SSH prompt modal.
type sshPromptState struct {
	server  string
	prompt  string
	input   []rune
	masked  bool        // passphrase/password -> mask input
	hostKey bool        // host key verification -> y/n only
	respond chan string // send response (close to cancel)
}

// Message types for lazy connections.
type connectServerMsg struct{ name string }
type disconnectServerMsg struct{ name string }
type sshPromptMsg struct {
	server  string
	prompt  string
	respond chan string
}
type connectDoneMsg struct {
	server string
	client *Client
	tunnel *Tunnel
	err    error
}

type sessionContainersMsg struct {
	server     string
	containers []protocol.ContainerInfo
}
type trackingDoneMsg struct {
	server string
}
type ruleCountMsg struct {
	server string
	count  int
}

// birdBlinkResetMsg signals the bird eye should reopen.
type birdBlinkResetMsg struct{}

// Backfill messages.
type metricsBackfillMsg struct {
	server    string
	resp      *protocol.QueryMetricsResp
	rangeHist bool // true if this was a historical (non-live) request
}

// timeWindow represents a graph time window preset.
type timeWindow struct {
	label   string
	seconds int64 // 0 = live streaming
}

var timeWindows = []timeWindow{
	{"Live", 0},
	{"1h", 3600},
	{"6h", 6 * 3600},
	{"12h", 12 * 3600},
	{"24h", 24 * 3600},
	{"3d", 3 * 86400},
	{"7d", 7 * 86400},
}

// view selects which screen the app is showing.
type view int

const (
	viewDashboard view = iota
	viewDetail
	viewAlerts
)

// App is the root Bubbletea model.
type App struct {
	sessions      map[string]*Session
	sessionOrder  []string // sorted server names
	activeSession string
	width         int
	height        int
	theme         Theme
	display       DisplayConfig
	err           error

	// Active view.
	view view

	// Dashboard state.
	cursor    int
	groups    []containerGroup
	collapsed map[string]bool // collapsed project groups
	windowIdx int             // index into timeWindows (0 = Live)

	// Server switcher.
	switcher       bool
	switcherCursor int

	spinnerFrame int
	birdBlink    bool // true = bird eye closed (data just arrived)
	helpModal    bool

	// Connection lifecycle.
	ctx              *appCtx
	sshPrompt        *sshPromptState
	connError        string // non-empty = show error dialog over switcher
	autoConnectQueue []string
	connecting       string
}

// NewApp creates the root model with one or more sessions.
func NewApp(sessions map[string]*Session, display DisplayConfig) App {
	order := make([]string, 0, len(sessions))
	for name := range sessions {
		order = append(order, name)
	}
	sort.Strings(order)

	// Default to first auto-connect server, or first server overall.
	active := ""
	showSwitcher := true
	var autoQueue []string
	for _, name := range order {
		if active == "" {
			active = name
		}
		if sessions[name].Config.AutoConnect {
			if showSwitcher {
				active = name // first auto-connect becomes active
			}
			showSwitcher = false
			autoQueue = append(autoQueue, name)
		}
	}

	return App{
		sessions:         sessions,
		sessionOrder:     order,
		activeSession:    active,
		switcher:         showSwitcher,
		autoConnectQueue: autoQueue,
		theme:            DefaultTheme(),
		display:          display,
		collapsed:     make(map[string]bool),
		ctx:           &appCtx{},
	}
}

// SetProgram stores the tea.Program reference in the shared appCtx.
func (a *App) SetProgram(p *tea.Program) {
	a.ctx.prog = p
}

// Err returns the application-level error, if any.
func (a App) Err() error { return a.err }

// hasActiveSubModal returns true if a view-level modal is open that should
// capture keys before the global help handler.
func (a *App) hasActiveSubModal() bool {
	s := a.session()
	if s == nil {
		return false
	}
	if a.view == viewDetail {
		det := &s.Detail
		return det.expandModal != nil || det.filterModal != nil || det.infoOverlay
	}
	if a.view == viewAlerts {
		return s.AlertsView.silenceModal != nil
	}
	return a.switcher
}

// session returns the currently active session, or nil.
func (a *App) session() *Session {
	return a.sessions[a.activeSession]
}

func (a App) Init() tea.Cmd {
	var cmds []tea.Cmd
	cmds = append(cmds, spinnerTick())

	// Subscribe already-connected sessions.
	for _, s := range a.sessions {
		if s.Client != nil && s.ConnState == ConnReady {
			cmds = append(cmds, subscribeAll(s.Client, a.windowSeconds()))
		}
	}

	// Auto-connect queue is built in NewApp; kick off the first connection.
	if cmd := a.processAutoConnectQueue(); cmd != nil {
		cmds = append(cmds, cmd)
	}

	return tea.Batch(cmds...)
}

func (a *App) processAutoConnectQueue() tea.Cmd {
	if a.connecting != "" {
		return nil
	}
	for len(a.autoConnectQueue) > 0 {
		name := a.autoConnectQueue[0]
		a.autoConnectQueue = a.autoConnectQueue[1:]
		s := a.sessions[name]
		if s != nil && s.ConnState == ConnNone {
			return func() tea.Msg { return connectServerMsg{name: name} }
		}
	}
	return nil
}

// subscribeAll subscribes to streaming topics, queries containers, and
// backfills graph history.
func subscribeAll(c *Client, windowSec int64) tea.Cmd {
	return tea.Batch(
		subscribeAndQueryContainers(c),
		backfillMetrics(c, windowSec),
		queryRuleCount(c),
	)
}

func queryRuleCount(c *Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rules, err := c.QueryAlertRules(ctx)
		if err != nil {
			return nil
		}
		return ruleCountMsg{server: c.server, count: len(rules)}
	}
}

func subscribeAndQueryContainers(c *Client) tea.Cmd {
	return func() tea.Msg {
		if err := c.Subscribe(protocol.TypeSubscribeMetrics, nil); err != nil {
			return ConnErrMsg{Err: fmt.Errorf("subscribe metrics: %w", err), Server: c.server}
		}
		if err := c.Subscribe(protocol.TypeSubscribeAlerts, nil); err != nil {
			return ConnErrMsg{Err: fmt.Errorf("subscribe alerts: %w", err), Server: c.server}
		}
		if err := c.Subscribe(protocol.TypeSubscribeContainers, nil); err != nil {
			return ConnErrMsg{Err: fmt.Errorf("subscribe containers: %w", err), Server: c.server}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		containers, err := c.QueryContainers(ctx)
		if err != nil {
			return ConnErrMsg{Err: fmt.Errorf("query containers: %w", err), Server: c.server}
		}
		return sessionContainersMsg{server: c.server, containers: containers}
	}
}

func queryContainersCmd(c *Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		containers, err := c.QueryContainers(ctx)
		if err != nil {
			return nil
		}
		return sessionContainersMsg{server: c.server, containers: containers}
	}
}

// windowSeconds returns the seconds value for the current time window (0 = Live).
func (a *App) windowSeconds() int64 {
	return timeWindows[a.windowIdx].seconds
}

// windowLabel returns the display label for the current time window.
func (a *App) windowLabel() string {
	return timeWindows[a.windowIdx].label
}

// tsFormat returns the combined date+time format string for timestamps.
func (a *App) tsFormat() string {
	return a.display.DateFormat + " " + a.display.TimeFormat
}

// backfillMetrics fetches historical host metrics to populate graphs.
// seconds=0 uses the default live backfill (last ~100 minutes).
// seconds>0 requests server-side downsampling to histBufSize points.
func backfillMetrics(c *Client, seconds int64) tea.Cmd {
	return func() tea.Msg {
		timeout := 5 * time.Second
		hist := seconds > 0
		if hist {
			timeout = 15 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		now := time.Now().Unix()
		rangeSec := int64(histBufSize * 10) // 600 points * 10s default interval
		points := 0
		if hist {
			rangeSec = seconds
			points = histBufSize
		}
		start := now - rangeSec

		resp, err := c.QueryMetrics(ctx, &protocol.QueryMetricsReq{
			Start:  start,
			End:    now,
			Points: points,
		})
		if err != nil {
			return nil // Non-critical: streaming will fill graphs.
		}
		return metricsBackfillMsg{
			server:    c.server,
			resp:      resp,
			rangeHist: hist,
		}
	}
}

// handleMetricsBackfill populates host ring buffers from historical data.
func handleMetricsBackfill(s *Session, resp *protocol.QueryMetricsResp, rangeHist bool) {
	if rangeHist {
		cpuBuf := NewRingBuffer[float64](histBufSize)
		memBuf := NewRingBuffer[float64](histBufSize)
		for _, h := range resp.Host {
			cpuBuf.Push(h.CPUPercent)
			memBuf.Push(h.MemPercent)
		}
		s.HostCPUHist = cpuBuf
		s.HostMemHist = memBuf
	} else {
		for _, h := range resp.Host {
			s.HostCPUHist.Push(h.CPUPercent)
			s.HostMemHist.Push(h.MemPercent)
		}
	}
}

func connectServerCmd(name string, cfg ServerConfig, appctx *appCtx, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		remoteSock := cfg.Socket
		if remoteSock == "" {
			remoteSock = "/run/tori/tori.sock"
		}

		var tunnel *Tunnel
		var sockPath string

		if cfg.Host != "" {
			promptFn := func(prompt string) (string, error) {
				ch := make(chan string, 1)
				if appctx.prog == nil {
					return "", errors.New("no program")
				}
				appctx.prog.Send(sshPromptMsg{
					server:  name,
					prompt:  prompt,
					respond: ch,
				})
				resp, ok := <-ch
				if !ok {
					return "", errors.New("cancelled")
				}
				return resp, nil
			}

			var err error
			tunnel, err = NewTunnelAskpass(cfg.Host, remoteSock, promptFn, SSHOptions{
				Port:         cfg.Port,
				IdentityFile: cfg.IdentityFile,
			})
			if err != nil {
				return connectDoneMsg{server: name, err: fmt.Errorf("tunnel: %w", err)}
			}

			if err := tunnel.WaitReady(ctx); err != nil {
				tunnel.Close()
				return connectDoneMsg{server: name, err: fmt.Errorf("tunnel: %w", err)}
			}
			sockPath = tunnel.LocalSocket()
		} else {
			sockPath = remoteSock
		}

		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
		if err != nil {
			if tunnel != nil {
				tunnel.Close()
			}
			return connectDoneMsg{server: name, err: fmt.Errorf("dial: %w", err)}
		}

		client := NewClient(conn, name)
		return connectDoneMsg{server: name, client: client, tunnel: tunnel}
	}
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		// Set terminal title.
		s := a.session()
		if s != nil {
			return a, tea.SetWindowTitle(fmt.Sprintf("tori —(•)> %s", s.Name))
		}
		return a, nil

	case sshPromptMsg:
		prompt := msg.prompt
		masked := isPasswordPrompt(prompt)
		hostKey := isHostKeyPrompt(prompt)
		a.sshPrompt = &sshPromptState{
			server:  msg.server,
			prompt:  prompt,
			masked:  masked,
			hostKey: hostKey,
			respond: msg.respond,
		}
		return a, nil

	case connectServerMsg:
		s := a.sessions[msg.name]
		if s == nil || s.ConnState != ConnNone {
			return a, a.processAutoConnectQueue()
		}
		s.ConnState = ConnConnecting
		s.ConnMsg = "connecting..."
		a.connecting = msg.name
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		s.connectCancel = cancel
		return a, connectServerCmd(msg.name, s.Config, a.ctx, ctx)

	case connectDoneMsg:
		return a.handleConnectDone(msg)

	case disconnectServerMsg:
		s := a.sessions[msg.name]
		if s == nil {
			return a, nil
		}
		if s.Client != nil {
			s.Client.Close()
			s.Client = nil
		}
		if s.Tunnel != nil {
			s.Tunnel.Close()
			s.Tunnel = nil
		}
		s.ConnState = ConnNone
		s.ConnMsg = "disconnected"
		s.Err = nil
		s.Host = nil
		s.Disks = nil
		s.Containers = nil
		s.ContInfo = nil
		s.Alerts = make(map[int64]*protocol.AlertEvent)
		a.groups = nil
		return a, nil

	case ConnErrMsg:
		if len(a.sessions) == 1 {
			a.err = msg.Err
			return a, tea.Quit
		}
		if s := a.sessions[msg.Server]; s != nil {
			s.Err = msg.Err
			s.ConnState = ConnError
			s.ConnMsg = msg.Err.Error()
		}
		return a, nil

	case MetricsMsg:
		if s := a.sessions[msg.Server]; s != nil {
			s.Host = msg.Host
			s.Disks = msg.Disks
			s.Containers = msg.Containers
			s.Rates.Update(msg.Timestamp, msg.Networks, msg.Containers)

			// Only push to ring buffers in live mode; historical windows
			// show a static snapshot from the backfill query.
			if msg.Host != nil && a.windowSeconds() == 0 {
				s.HostCPUHist.Push(msg.Host.CPUPercent)
				s.HostMemHist.Push(msg.Host.MemPercent)
			}

			if msg.Server == a.activeSession {
				a.groups = buildGroups(msg.Containers, s.ContInfo)
				// Push live metrics to detail view ring buffers.
				if a.view == viewDetail && a.windowSeconds() == 0 {
					s.Detail.pushLiveMetrics(msg.Containers)
				}
			}
		}
		if !a.birdBlink {
			a.birdBlink = true
			return a, tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
				return birdBlinkResetMsg{}
			})
		}
		return a, nil

	case LogMsg:
		if s := a.sessions[msg.Server]; s != nil {
			if a.view == viewDetail && msg.Server == a.activeSession {
				s.Detail.onStreamEntry(msg.LogEntryMsg)
			}
		}
		return a, nil

	case detailLogQueryMsg:
		if s := a.session(); s != nil {
			s.Detail.handleBackfill(msg)
		}
		return a, nil

	case detailMetricsQueryMsg:
		if s := a.session(); s != nil {
			s.Detail.handleMetricsBackfill(msg)
		}
		return a, nil

	case birdBlinkResetMsg:
		a.birdBlink = false
		return a, nil

	case metricsBackfillMsg:
		if s := a.sessions[msg.server]; s != nil && msg.resp != nil {
			if msg.resp.RetentionDays > 0 {
				s.RetentionDays = msg.resp.RetentionDays
			}
			handleMetricsBackfill(s, msg.resp, msg.rangeHist)
			s.BackfillPending = false
		}
		return a, nil

	case AlertEventMsg:
		if s := a.sessions[msg.Server]; s != nil {
			if msg.State == "resolved" {
				delete(s.Alerts, msg.ID)
			} else {
				if len(s.Alerts) >= 1000 {
					var oldestID int64
					var oldestTS int64
					for id, e := range s.Alerts {
						if oldestTS == 0 || e.FiredAt < oldestTS {
							oldestID = id
							oldestTS = e.FiredAt
						}
					}
					delete(s.Alerts, oldestID)
				}
				e := msg.AlertEvent
				s.Alerts[msg.ID] = &e
			}
		}
		return a, nil

	case ContainerEventMsg:
		if s := a.sessions[msg.Server]; s != nil {
			return a, queryContainersCmd(s.Client)
		}
		return a, nil

	case sessionContainersMsg:
		if s := a.sessions[msg.server]; s != nil {
			s.ContInfo = msg.containers
			if msg.server == a.activeSession {
				a.groups = buildGroups(s.Containers, s.ContInfo)
			}
		}
		return a, nil

	case trackingDoneMsg:
		if s := a.sessions[msg.server]; s != nil {
			return a, queryContainersCmd(s.Client)
		}
		return a, nil

	case ruleCountMsg:
		if s := a.sessions[msg.server]; s != nil {
			s.RuleCount = msg.count
		}
		return a, nil

	case alertsDataMsg:
		if s := a.sessions[msg.server]; s != nil {
			s.AlertsView.rules = msg.rules
			s.AlertsView.resolved = msg.resolved
			s.AlertsView.loaded = true
			s.RuleCount = len(msg.rules)
		}
		return a, nil

	case alertAckDoneMsg:
		if s := a.sessions[msg.server]; s != nil && s.Client != nil {
			return a, queryAlertsData(s.Client, msg.server)
		}
		return a, nil

	case alertSilenceDoneMsg:
		if s := a.sessions[msg.server]; s != nil && s.Client != nil {
			return a, queryAlertsData(s.Client, msg.server)
		}
		return a, nil

	case spinnerTickMsg:
		a.spinnerFrame++
		return a, spinnerTick()

	case tea.KeyMsg:
		if a.sshPrompt != nil {
			return a.handleSSHPromptInput(msg)
		}
		if a.connError != "" {
			a.connError = ""
			return a, nil
		}
		return a.handleKey(msg)
	}
	return a, nil
}

func (a *App) handleConnectDone(msg connectDoneMsg) (App, tea.Cmd) {
	s := a.sessions[msg.server]
	if s == nil {
		return *a, nil
	}

	if a.connecting == msg.server {
		a.connecting = ""
	}

	if s.connectCancel != nil {
		s.connectCancel()
		s.connectCancel = nil
	}

	if msg.err != nil {
		s.ConnState = ConnNone
		s.ConnMsg = ""
		a.connError = msg.err.Error()
		a.switcher = true
		return *a, a.processAutoConnectQueue()
	}

	s.Client = msg.client
	s.Tunnel = msg.tunnel
	s.ConnState = ConnReady
	s.ConnMsg = "connected"
	s.Err = nil
	if a.switcher && a.activeSession == msg.server {
		a.switcher = false
	}

	if a.ctx.prog != nil {
		s.Client.SetProgram(a.ctx.prog)
	}

	s.BackfillPending = true

	var cmds []tea.Cmd
	cmds = append(cmds, subscribeAll(s.Client, a.windowSeconds()))

	if cmd := a.processAutoConnectQueue(); cmd != nil {
		cmds = append(cmds, cmd)
	}

	return *a, tea.Batch(cmds...)
}

func (a App) handleSSHPromptInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	p := a.sshPrompt

	switch {
	case key == "enter":
		if p.hostKey {
			select {
			case p.respond <- "yes":
			default:
			}
		} else {
			select {
			case p.respond <- string(p.input):
			default:
			}
		}
		a.sshPrompt = nil
		return a, nil

	case key == "esc", key == "ctrl+c":
		close(p.respond)
		a.sshPrompt = nil
		return a, nil

	case key == "backspace":
		if len(p.input) > 0 && !p.hostKey {
			p.input = p.input[:len(p.input)-1]
		}
		return a, nil

	default:
		if p.hostKey {
			if key == "y" || key == "Y" {
				select {
				case p.respond <- "yes":
				default:
				}
				a.sshPrompt = nil
				return a, nil
			}
			if key == "n" || key == "N" {
				close(p.respond)
				a.sshPrompt = nil
				return a, nil
			}
			return a, nil
		}

		if len(key) == 1 && len(p.input) < 256 {
			p.input = append(p.input, rune(key[0]))
		}
		return a, nil
	}
}

func isPasswordPrompt(prompt string) bool {
	lower := strings.ToLower(prompt)
	return strings.Contains(lower, "passphrase") ||
		strings.Contains(lower, "password")
}

func isHostKeyPrompt(prompt string) bool {
	lower := strings.ToLower(prompt)
	return strings.Contains(lower, "authenticity") ||
		strings.Contains(lower, "fingerprint") ||
		strings.Contains(lower, "yes/no")
}

func (a App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Global quit.
	if key == "q" || key == "ctrl+c" {
		if a.helpModal {
			a.helpModal = false
			return a, nil
		}
		for _, s := range a.sessions {
			if s.connectCancel != nil {
				s.connectCancel()
			}
		}
		return a, tea.Quit
	}

	// Help modal blocks all input.
	if a.helpModal {
		if key == "?" || key == "esc" {
			a.helpModal = false
		}
		return a, nil
	}

	// Open help modal (unless a sub-modal is active).
	if key == "?" && !a.hasActiveSubModal() {
		a.helpModal = true
		return a, nil
	}

	// Detail view captures its own keys.
	if a.view == viewDetail {
		return a.handleDetailKey(msg)
	}

	// Alerts view captures its own keys.
	if a.view == viewAlerts {
		return a.handleAlertsKey(msg)
	}

	// Server switcher.
	if a.switcher {
		return a.handleSwitcherKey(key)
	}

	// Zoom time window.
	if key == "+" || key == "=" || key == "-" {
		if cmd := a.handleZoom(key); cmd != nil {
			return a, cmd
		}
		return a, nil
	}

	switch key {
	case "S":
		if len(a.sessions) > 1 {
			a.switcher = true
			// Set switcher cursor to current active session.
			for i, name := range a.sessionOrder {
				if name == a.activeSession {
					a.switcherCursor = i
					break
				}
			}
		}
		return a, nil

	case "j", "down":
		items := buildSelectableItems(a.groups, a.collapsed)
		max := len(items) - 1
		if max < 0 {
			max = 0
		}
		if a.cursor < max {
			a.cursor++
		}
		return a, nil

	case "k", "up":
		if a.cursor > 0 {
			a.cursor--
		}
		return a, nil

	case " ":
		items := buildSelectableItems(a.groups, a.collapsed)
		if a.cursor >= 0 && a.cursor < len(items) && items[a.cursor].isProject {
			name := a.groups[items[a.cursor].groupIdx].name
			a.collapsed[name] = !a.collapsed[name]
			// Rebuild items after toggle and clamp cursor.
			newItems := buildSelectableItems(a.groups, a.collapsed)
			if a.cursor >= len(newItems) {
				a.cursor = len(newItems) - 1
			}
			if a.cursor < 0 {
				a.cursor = 0
			}
		}
		return a, nil

	case "t":
		return a, a.toggleTracking()

	case "enter":
		return a.enterDetail()

	case "1":
		// Already on dashboard.
		return a, nil

	case "2":
		return a, a.enterAlerts()
	}

	return a, nil
}

func (a *App) handleSwitcherKey(key string) (App, tea.Cmd) {
	switch key {
	case "j", "down":
		if a.switcherCursor < len(a.sessionOrder)-1 {
			a.switcherCursor++
		}
	case "k", "up":
		if a.switcherCursor > 0 {
			a.switcherCursor--
		}
	case "enter":
		name := a.sessionOrder[a.switcherCursor]
		a.activeSession = name
		// Rebuild groups for newly selected session.
		if s := a.session(); s != nil {
			a.groups = buildGroups(s.Containers, s.ContInfo)
			a.cursor = 0
		}
		s := a.sessions[name]
		if s == nil {
			break
		}
		switch s.ConnState {
		case ConnNone:
			// Start connection, keep switcher open.
			return *a, func() tea.Msg { return connectServerMsg{name: name} }
		case ConnConnecting, ConnSSH:
			// Connection in progress — ignore.
		default:
			// Already connected — close switcher.
			a.switcher = false
		}
	case "esc", "S":
		a.switcher = false
	}
	return *a, nil
}

// handleZoom adjusts the time window and triggers a backfill.
func (a *App) handleZoom(key string) tea.Cmd {
	s := a.session()
	if s == nil || s.Client == nil {
		return nil
	}
	prev := a.windowIdx
	switch key {
	case "+", "=":
		for i := a.windowIdx + 1; i < len(timeWindows); i++ {
			w := timeWindows[i]
			if s.RetentionDays > 0 && w.seconds > int64(s.RetentionDays)*86400 {
				break
			}
			a.windowIdx = i
			break
		}
	case "-":
		if a.windowIdx > 0 {
			a.windowIdx--
		}
	}
	if a.windowIdx == prev {
		return nil
	}
	// Clear history so graphs show fresh data from the backfill.
	s.HostCPUHist = NewRingBuffer[float64](histBufSize)
	s.HostMemHist = NewRingBuffer[float64](histBufSize)
	s.BackfillPending = true

	cmds := []tea.Cmd{backfillMetrics(s.Client, a.windowSeconds())}

	// Re-backfill detail metrics when in detail view.
	if a.view == viewDetail {
		s.Detail.metricsBackfilled = false
		s.Detail.metricsBackfillPending = false
		if cmd := s.Detail.onSwitch(s.Client, a.windowSeconds(), s.RetentionDays); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	return tea.Batch(cmds...)
}

func (a *App) toggleTracking() tea.Cmd {
	s := a.session()
	if s == nil || s.Client == nil {
		return nil
	}

	items := buildSelectableItems(a.groups, a.collapsed)
	if a.cursor < 0 || a.cursor >= len(items) {
		return nil
	}

	item := items[a.cursor]
	group := a.groups[item.groupIdx].name

	if item.isProject {
		// Toggle entire project.
		tracked := isProjectTracked(group, s.ContInfo)
		client := s.Client
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client.SetTracking(ctx, "", group, !tracked)
			return trackingDoneMsg{server: client.server}
		}
	}

	// Single container.
	c := containerAtCursor(a.groups, items, a.cursor)
	if c == nil {
		return nil
	}
	name := containerNameByID(c.ID, s.ContInfo)
	tracked := isContainerTracked(c.ID, s.ContInfo)
	client := s.Client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client.SetTracking(ctx, name, "", !tracked)
		return trackingDoneMsg{server: client.server}
	}
}

func isProjectTracked(project string, contInfo []protocol.ContainerInfo) bool {
	for _, ci := range contInfo {
		if ci.Project == project && ci.Tracked {
			return true
		}
	}
	return false
}

func isContainerTracked(id string, contInfo []protocol.ContainerInfo) bool {
	for _, ci := range contInfo {
		if ci.ID == id {
			return ci.Tracked
		}
	}
	return false
}

func containerNameByID(id string, contInfo []protocol.ContainerInfo) string {
	for _, ci := range contInfo {
		if ci.ID == id {
			return ci.Name
		}
	}
	return ""
}

func renderConnErrorModal(a *App, width, height int) string {
	theme := &a.theme
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)
	titleStyle := lipgloss.NewStyle().Foreground(theme.Critical).Bold(true)

	modalW := 60
	innerW := modalW - 6

	// Sanitize: strip carriage returns and tabs from SSH stderr output.
	errText := strings.NewReplacer("\r", "", "\t", "  ").Replace(a.connError)

	var lines []string
	for _, chunk := range wrapText(errText, innerW) {
		lines = append(lines, muted.Render(Truncate(chunk, innerW)))
	}

	return (dialogLayout{
		title:      "Connection Error",
		titleStyle: &titleStyle,
		width:      modalW,
		lines:      lines,
		tips:       dialogTips(theme, "any key", "dismiss"),
	}).render(width, height, theme)
}

func (a *App) renderSSHPromptModal(width, height int) string {
	p := a.sshPrompt
	theme := &a.theme
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

	modalW := 72
	innerW := modalW - 2

	var lines []string
	lines = append(lines, muted.Render(Truncate(p.server, innerW-4)))
	lines = append(lines, "")

	for _, chunk := range wrapText(p.prompt, innerW-4) {
		lines = append(lines, chunk)
	}

	var tips string
	if p.hostKey {
		tips = dialogTips(theme, "y", "accept", "n", "reject")
	} else {
		lines = append(lines, "")

		display := string(p.input)
		if p.masked {
			display = strings.Repeat("*", len(p.input))
		}
		cursor := lipgloss.NewStyle().Foreground(theme.Accent).Render("█")
		prompt := muted.Render("> ")
		promptW := lipgloss.Width(prompt)
		fieldW := innerW - promptW - 4
		if len(display) > fieldW {
			display = display[len(display)-fieldW:]
		}
		lines = append(lines, prompt+display+cursor)

		tips = dialogTips(theme, "enter", "submit", "esc", "cancel")
	}

	return (dialogLayout{
		title: "SSH",
		width: modalW,
		lines: lines,
		tips:  tips,
	}).render(width, height, theme)
}

func (a App) View() string {
	if a.err != nil {
		return fmt.Sprintf("Error: %v\n", a.err)
	}
	if a.width == 0 || a.height == 0 {
		return SpinnerView(a.spinnerFrame, "Connecting...", &a.theme)
	}

	s := a.session()
	if s == nil {
		return "No sessions available"
	}
	if s.Err != nil && len(a.sessions) == 1 {
		return fmt.Sprintf("Error [%s]: %v\n", s.Name, s.Err)
	}

	var content string
	switch a.view {
	case viewDetail:
		content = renderDetail(&a, s, a.width, a.height)
	case viewAlerts:
		content = renderAlerts(&a, s, a.width, a.height)
	default:
		content = renderDashboard(&a, s, a.width, a.height)
	}

	// Layer modals independently so SSH prompt renders on top.
	if a.switcher {
		modal := renderSwitcher(&a, a.width, a.height)
		content = Overlay(content, modal, a.width, a.height)
	}
	if a.helpModal {
		modal := renderHelpModal(&a, s, a.width, a.height)
		content = Overlay(content, modal, a.width, a.height)
	}
	if a.connError != "" {
		modal := renderConnErrorModal(&a, a.width, a.height)
		content = Overlay(content, modal, a.width, a.height)
	}
	if a.sshPrompt != nil {
		modal := a.renderSSHPromptModal(a.width, a.height)
		content = Overlay(content, modal, a.width, a.height)
	}

	return content
}
