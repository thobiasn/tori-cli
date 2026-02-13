package tui

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

	"github.com/thobiasn/rook/internal/protocol"
)

type view int

const (
	viewDashboard view = iota
	viewDetail
	viewAlerts
)

// timeWindow represents a graph time window preset.
type timeWindow struct {
	label   string // e.g. "Live", "1h", "7d"
	seconds int64  // 0 = live streaming
}

// ringBufSize is the number of data points in each ring buffer.
const ringBufSize = 600

var timeWindows = []timeWindow{
	{"Live", 0},
	{"1h", 3600},
	{"6h", 6 * 3600},
	{"12h", 12 * 3600},
	{"24h", 24 * 3600},
	{"3d", 3 * 86400},
	{"7d", 7 * 86400},
}

// appCtx is shared mutable state between the bubbletea copy of App and
// the connect goroutines. Since App is a value type, we use a pointer
// to a struct that both copies reference.
type appCtx struct {
	prog *tea.Program
}

// sshPromptState holds the state of an active SSH prompt modal.
type sshPromptState struct {
	server  string
	prompt  string
	input   []rune
	masked  bool        // passphrase/password → mask input
	hostKey bool        // host key verification → y/n only
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

// App is the root Bubbletea model.
type App struct {
	sessions         map[string]*Session
	sessionOrder     []string // sorted server names for deterministic iteration
	activeSession    string
	width            int
	height           int
	active           view
	windowIdx        int // index into timeWindows (0 = Live)
	theme            Theme
	err              error
	showHelp         bool
	showServerPicker bool
	dashFocus        dashFocus // focusServers (default) or focusContainers
	serverCursor     int       // index into sessionOrder

	// Lazy connection state.
	ctx              *appCtx          // shared program reference
	sshPrompt        *sshPromptState  // active SSH prompt modal (nil = none)
	autoConnectQueue []string         // servers to auto-connect sequentially
	connecting       string           // server currently connecting ("" = idle)
}

// session returns the currently active session, or nil.
func (a *App) session() *Session {
	return a.sessions[a.activeSession]
}

// NewApp creates the root model with one or more sessions.
func NewApp(sessions map[string]*Session) App {
	order := make([]string, 0, len(sessions))
	for name := range sessions {
		order = append(order, name)
	}
	sort.Strings(order)

	active := ""
	if len(order) > 0 {
		active = order[0]
	}

	return App{
		sessions:      sessions,
		sessionOrder:  order,
		activeSession: active,
		theme:         DefaultTheme(),
		ctx:           &appCtx{},
	}
}

// SetProgram stores the tea.Program reference in the shared appCtx.
// Must be called after tea.NewProgram and before p.Run().
func (a *App) SetProgram(p *tea.Program) {
	a.ctx.prog = p
}

// subscribeAll subscribes to all streaming topics, queries containers, and
// backfills graph history from the last 30 minutes of stored metrics.
func subscribeAll(c *Client) tea.Cmd {
	return tea.Batch(
		subscribeAndQueryContainers(c),
		backfillMetrics(c, 0),
	)
}

func subscribeAndQueryContainers(c *Client) tea.Cmd {
	return func() tea.Msg {
		if err := c.Subscribe(protocol.TypeSubscribeMetrics, nil); err != nil {
			return ConnErrMsg{Err: fmt.Errorf("subscribe metrics: %w", err), Server: c.server}
		}
		if err := c.Subscribe(protocol.TypeSubscribeLogs, nil); err != nil {
			return ConnErrMsg{Err: fmt.Errorf("subscribe logs: %w", err), Server: c.server}
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

type sessionContainersMsg struct {
	server     string
	containers []protocol.ContainerInfo
}
type trackingDoneMsg struct {
	server string
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

func (a App) Init() tea.Cmd {
	var cmds []tea.Cmd

	// Subscribe already-connected sessions.
	for _, s := range a.sessions {
		if s.Client != nil && s.ConnState == ConnReady {
			cmds = append(cmds, subscribeAll(s.Client))
		}
	}

	// Build auto-connect queue from sessions with AutoConnect && ConnNone.
	for _, name := range a.sessionOrder {
		s := a.sessions[name]
		if s.Config.AutoConnect && s.ConnState == ConnNone {
			a.autoConnectQueue = append(a.autoConnectQueue, name)
		}
	}

	// Start processing the queue.
	if cmd := a.processAutoConnectQueue(); cmd != nil {
		cmds = append(cmds, cmd)
	}

	return tea.Batch(cmds...)
}

// processAutoConnectQueue pops the next ConnNone server from the queue and
// starts connecting. Returns nil if nothing to do or already connecting.
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

// connectServerCmd returns a tea.Cmd that connects to a server in a goroutine.
func connectServerCmd(name string, cfg ServerConfig, appctx *appCtx) tea.Cmd {
	return func() tea.Msg {
		remoteSock := cfg.Socket
		if remoteSock == "" {
			remoteSock = "/run/rook/rook.sock"
		}

		var tunnel *Tunnel
		var sockPath string

		if cfg.Host != "" {
			// SSH server — use askpass tunnel.
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

			// Wait for tunnel with generous timeout.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := tunnel.WaitReady(ctx); err != nil {
				tunnel.Close()
				return connectDoneMsg{server: name, err: fmt.Errorf("tunnel: %w", err)}
			}
			sockPath = tunnel.LocalSocket()
		} else {
			// Local socket — connect directly.
			sockPath = remoteSock
		}

		conn, err := net.Dial("unix", sockPath)
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
		return a, nil

	case connectServerMsg:
		s := a.sessions[msg.name]
		if s == nil || s.ConnState != ConnNone {
			return a, a.processAutoConnectQueue()
		}
		s.ConnState = ConnConnecting
		s.ConnMsg = "connecting..."
		a.connecting = msg.name
		return a, connectServerCmd(msg.name, s.Config, a.ctx)

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
		s.Dash.groups = nil
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

	case connectDoneMsg:
		return a.handleConnectDone(msg)

	case ConnErrMsg:
		// Only quit on error for single-server mode.
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

	case sessionContainersMsg:
		if s := a.sessions[msg.server]; s != nil {
			s.ContInfo = msg.containers
			// Rebuild groups so untracked containers appear as stubs immediately.
			s.Dash.groups = buildGroups(s.Containers, s.ContInfo)
		}
		return a, nil

	case metricsBackfillMsg:
		if s := a.sessions[msg.server]; s != nil && msg.resp != nil {
			if msg.resp.RetentionDays > 0 {
				s.RetentionDays = msg.resp.RetentionDays
			}
			handleMetricsBackfill(s, msg.resp, msg.start, msg.end, msg.rangeHist)
		}
		return a, nil

	case backfillRetryMsg:
		if s := a.sessions[msg.server]; s != nil {
			return a, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
				return backfillRetryTickMsg(msg)
			})
		}
		return a, nil

	case backfillRetryTickMsg:
		if s := a.sessions[msg.server]; s != nil {
			return a, backfillMetrics(s.Client, msg.seconds)
		}
		return a, nil

	case MetricsMsg:
		if s := a.sessions[msg.Server]; s != nil {
			return a, a.handleSessionMetrics(s, msg.MetricsUpdate)
		}
		return a, nil

	case LogMsg:
		if s := a.sessions[msg.Server]; s != nil {
			s.Detail.onStreamEntry(msg.LogEntryMsg)
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
			var cmds []tea.Cmd

			// Auto-switch detail view BEFORE processing the log entry
			// so the "start" event is captured in the new container's logs.
			if a.active == viewDetail && msg.State == "running" {
				if cmd := a.handleDetailAutoSwitch(s, msg.ContainerEvent); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}

			entry := containerEventToLog(msg.ContainerEvent)
			s.Detail.onStreamEntry(entry)

			// Re-query ContInfo so the dashboard shows correct project
			// grouping and tracked state for new/removed containers.
			cmds = append(cmds, queryContainersCmd(s.Client))

			return a, tea.Batch(cmds...)
		}
		return a, nil

	case alertActionDoneMsg:
		if s := a.session(); s != nil {
			s.Alertv.stale = true
		}
		return a, nil

	case trackingDoneMsg:
		if s := a.sessions[msg.server]; s != nil {
			return a, queryContainersCmd(s.Client)
		}
		return a, nil

	case alertQueryMsg:
		if s := a.session(); s != nil {
			s.Alertv.alerts = msg.alerts
			s.Alertv.stale = false
		}
		return a, nil

	case detailLogQueryMsg:
		if s := a.session(); s != nil {
			s.Detail.handleBackfill(msg)
		}
		return a, nil

	case detailMetricsQueryMsg:
		if s := a.session(); s != nil && msg.resp != nil &&
			msg.containerID == s.Detail.containerID && msg.project == s.Detail.project {
			handleDetailMetricsBackfill(s, &s.Detail, msg.resp, msg.start, msg.end, msg.windowSec)
		}
		s := a.session()
		if s != nil {
			s.Detail.metricsBackfillPending = false
		}
		return a, nil

	case tea.KeyMsg:
		// SSH prompt modal intercepts all keys.
		if a.sshPrompt != nil {
			return a.handleSSHPromptInput(msg)
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

	if msg.err != nil {
		s.Err = msg.err
		s.ConnState = ConnError
		s.ConnMsg = msg.err.Error()
		return *a, a.processAutoConnectQueue()
	}

	s.Client = msg.client
	s.Tunnel = msg.tunnel
	s.ConnState = ConnReady
	s.ConnMsg = "connected"
	s.Err = nil

	// Start reading and subscribe.
	if a.ctx.prog != nil {
		s.Client.SetProgram(a.ctx.prog)
	}

	var cmds []tea.Cmd
	cmds = append(cmds, subscribeAll(s.Client))

	// Process next auto-connect item.
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
			// Host key prompt: Enter sends "yes".
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
			// Host key prompt: y sends "yes", n cancels.
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

		// Accumulate characters (max 256).
		if len(key) == 1 && len(p.input) < 256 {
			p.input = append(p.input, rune(key[0]))
		}
		return a, nil
	}
}

// isPasswordPrompt returns true if the prompt looks like a password/passphrase request.
func isPasswordPrompt(prompt string) bool {
	lower := strings.ToLower(prompt)
	return strings.Contains(lower, "passphrase") ||
		strings.Contains(lower, "password")
}

// isHostKeyPrompt returns true if the prompt looks like a host key verification.
func isHostKeyPrompt(prompt string) bool {
	lower := strings.ToLower(prompt)
	return strings.Contains(lower, "authenticity") ||
		strings.Contains(lower, "fingerprint") ||
		strings.Contains(lower, "yes/no")
}

func (a *App) renderSSHPromptModal(width, height int) string {
	p := a.sshPrompt
	theme := &a.theme
	muted := lipgloss.NewStyle().Foreground(theme.Muted)

	modalW := 60
	if modalW > width-4 {
		modalW = width - 4
	}
	innerW := modalW - 2

	var lines []string
	lines = append(lines, muted.Render(" "+Truncate(p.server, innerW-1)))
	lines = append(lines, "")

	// Wrap prompt text.
	for _, chunk := range wrapText(p.prompt, innerW-2) {
		lines = append(lines, " "+chunk)
	}
	lines = append(lines, "")

	if p.hostKey {
		lines = append(lines, " "+lipgloss.NewStyle().Foreground(theme.Warning).Render("y")+" Accept   "+lipgloss.NewStyle().Foreground(theme.Critical).Render("n")+" Reject")
	} else {
		// Input field.
		display := string(p.input)
		if p.masked {
			display = strings.Repeat("*", len(p.input))
		}
		cursor := lipgloss.NewStyle().Foreground(theme.Accent).Render("█")
		fieldW := innerW - 4
		if len(display) > fieldW {
			display = display[len(display)-fieldW:]
		}
		lines = append(lines, "  "+display+cursor)
	}

	content := strings.Join(lines, "\n")
	modalH := len(lines) + 2
	if modalH > height-2 {
		modalH = height - 2
	}

	modal := Box("SSH", content, modalW, modalH, theme)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

func (a *App) handleSessionMetrics(s *Session, m *protocol.MetricsUpdate) tea.Cmd {
	s.Host = m.Host
	s.Disks = m.Disks
	s.Containers = m.Containers
	s.Rates.Update(m.Timestamp, m.Networks, m.Containers)

	// Only push to ring buffers in live mode; historical windows show
	// a static snapshot from the backfill query.
	live := a.windowSeconds() == 0

	if m.Host != nil && live {
		s.HostCPUHistory.Push(m.Host.CPUPercent)
		s.HostMemHistory.Push(m.Host.MemPercent)
		pushMemHistories(s, m.Host)
	}

	// Per-container history + stale cleanup.
	current := make(map[string]bool, len(m.Containers))
	for _, c := range m.Containers {
		current[c.ID] = true
		if live {
			if _, ok := s.CPUHistory[c.ID]; !ok {
				// Transfer buffer from a previous container with the same service identity.
				if old := findServicePredecessor(c, s); old != "" {
					s.CPUHistory[c.ID] = s.CPUHistory[old]
					s.MemHistory[c.ID] = s.MemHistory[old]
					delete(s.CPUHistory, old)
					delete(s.MemHistory, old)
				} else {
					s.CPUHistory[c.ID] = NewRingBuffer[float64](ringBufSize)
					s.MemHistory[c.ID] = NewRingBuffer[float64](ringBufSize)
				}
			}
			s.CPUHistory[c.ID].Push(c.CPUPercent)
			s.MemHistory[c.ID].Push(float64(c.MemUsage))
		}
	}
	if live {
		for id := range s.CPUHistory {
			if !current[id] {
				delete(s.CPUHistory, id)
				delete(s.MemHistory, id)
			}
		}
	}

	// Rebuild container groups for dashboard.
	s.Dash.groups = buildGroups(m.Containers, s.ContInfo)
	return nil
}

func (a App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Global quit — cancel any in-flight connections.
	if key == "q" || key == "ctrl+c" {
		for _, s := range a.sessions {
			if s.connectCancel != nil {
				s.connectCancel()
			}
		}
		return a, tea.Quit
	}

	// Help toggle.
	if key == "?" {
		a.showHelp = !a.showHelp
		return a, nil
	}
	if a.showHelp {
		a.showHelp = false
		return a, nil
	}

	// Server picker.
	if a.showServerPicker {
		return a.handleServerPicker(key)
	}
	if key == "S" && len(a.sessions) > 1 && a.active != viewDashboard {
		a.showServerPicker = true
		return a, nil
	}

	// When detail search mode is active, only tab and esc are handled globally.
	detailSearchActive := a.active == viewDetail && a.session() != nil && a.session().Detail.searchMode

	// Zoom time window (+/- keys) — only on views with graphs.
	if !detailSearchActive && (key == "+" || key == "=" || key == "-") {
		if a.active == viewDashboard || a.active == viewDetail {
			if cmd := a.handleZoom(key); cmd != nil {
				return a, cmd
			}
			return a, nil
		}
	}

	// View switching.
	if !detailSearchActive {
		if cmd, ok := a.handleViewSwitch(key); ok {
			return a, cmd
		}
	}

	// Delegate to active view.
	s := a.session()
	if s == nil {
		return a, nil
	}
	switch a.active {
	case viewDashboard:
		return a, updateDashboard(&a, s, msg)
	case viewAlerts:
		return a, updateAlertView(&a, s, msg)
	case viewDetail:
		return a, updateDetail(&a, s, msg)
	}
	return a, nil
}

// handleDetailAutoSwitch detects when a new container starts with the same
// service identity as the one currently viewed in the detail view. If so, it
// switches the detail view to the new container and re-triggers backfills.
func (a *App) handleDetailAutoSwitch(s *Session, evt protocol.ContainerEvent) tea.Cmd {
	det := &s.Detail
	if det.containerID == "" || det.containerID == evt.ContainerID {
		return nil
	}
	if det.svcService == "" {
		return nil
	}

	// Compute the event's service identity using the same logic as the agent.
	evtProject, evtService := evt.Project, evt.Service
	if evtProject == "" || evtService == "" {
		// Non-compose fallback: use container name.
		evtProject = ""
		evtService = evt.Name
	}

	if evtProject != det.svcProject || evtService != det.svcService {
		return nil
	}

	// Match — switch to the new container.
	det.containerID = evt.ContainerID
	det.reset()
	return det.onSwitch(s.Client, a.windowSeconds(), s.RetentionDays)
}

// handleZoom adjusts the time window and triggers a backfill.
func (a *App) handleZoom(key string) tea.Cmd {
	s := a.session()
	if s == nil || s.Client == nil {
		return nil
	}
	prev := a.windowIdx
	switch key {
	case "+", "=": // zoom out (longer window)
		for i := a.windowIdx + 1; i < len(timeWindows); i++ {
			w := timeWindows[i]
			if s.RetentionDays > 0 && w.seconds > int64(s.RetentionDays)*86400 {
				break
			}
			a.windowIdx = i
			break
		}
	case "-": // zoom in (shorter window)
		if a.windowIdx > 0 {
			a.windowIdx--
		}
	}
	if a.windowIdx == prev {
		return nil
	}
	s.Detail.metricsBackfilled = false
	var cmds []tea.Cmd
	cmds = append(cmds, backfillMetrics(s.Client, timeWindows[a.windowIdx].seconds))
	if a.active == viewDetail {
		if cmd := s.Detail.onSwitch(s.Client, timeWindows[a.windowIdx].seconds, s.RetentionDays); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// windowLabel returns the label for the current time window.
func (a *App) windowLabel() string {
	return timeWindows[a.windowIdx].label
}

// windowSeconds returns the seconds value for the current time window (0 = Live).
func (a *App) windowSeconds() int64 {
	return timeWindows[a.windowIdx].seconds
}

func (a *App) handleServerPicker(key string) (App, tea.Cmd) {
	// Number keys 1-9 select a server.
	if key >= "1" && key <= "9" {
		idx := int(key[0]-'0') - 1
		if idx < len(a.sessionOrder) {
			prev := a.activeSession
			a.activeSession = a.sessionOrder[idx]
			a.serverCursor = idx
			if a.activeSession != prev {
				a.showServerPicker = false
				return *a, a.onViewSwitch()
			}
		}
	}
	a.showServerPicker = false
	return *a, nil
}

func (a *App) handleViewSwitch(key string) (tea.Cmd, bool) {
	prev := a.active
	switch key {
	case "tab":
		if a.active == viewDashboard || a.active == viewDetail {
			return nil, false // delegate to view-specific handler for focus toggle
		}
		a.active = viewDashboard
	case "1":
		a.active = viewDashboard
	case "2":
		a.active = viewAlerts
	default:
		return nil, false
	}
	if a.active == prev {
		return nil, true
	}
	return a.onViewSwitch(), true
}

func (a *App) onViewSwitch() tea.Cmd {
	s := a.session()
	if s == nil || s.Client == nil {
		return nil
	}
	switch a.active {
	case viewAlerts:
		return s.Alertv.onSwitch(s.Client)
	case viewDetail:
		return s.Detail.onSwitch(s.Client, a.windowSeconds(), s.RetentionDays)
	}
	return nil
}

// findServicePredecessor looks for an existing buffer entry with the same
// compose {project, service} identity as the new container. Returns the old
// container ID if found, empty string otherwise. This enables graph continuity
// across container restarts/redeploys.
func findServicePredecessor(c protocol.ContainerMetrics, s *Session) string {
	if c.Service == "" {
		return ""
	}
	// Check ContInfo for old containers matching the same service identity.
	for _, ci := range s.ContInfo {
		if ci.ID == c.ID {
			continue
		}
		if ci.Project == c.Project && ci.Service == c.Service {
			if _, ok := s.CPUHistory[ci.ID]; ok {
				return ci.ID
			}
		}
	}
	return ""
}

// Err returns the application-level error (e.g. connection lost), if any.
func (a App) Err() error { return a.err }

func (a App) View() string {
	if a.err != nil {
		return fmt.Sprintf("Error: %v\n", a.err)
	}
	if a.width == 0 || a.height == 0 {
		return "Connecting..."
	}

	s := a.session()
	if s == nil {
		return "No sessions available"
	}
	if s.Err != nil && len(a.sessions) == 1 {
		return fmt.Sprintf("Error [%s]: %v\n", s.Name, s.Err)
	}

	// Reserve 1 line for footer.
	contentH := a.height - 1
	if contentH < 1 {
		contentH = 1
	}

	var content string
	switch a.active {
	case viewDashboard:
		content = renderDashboard(&a, s, a.width, contentH)
	case viewAlerts:
		content = renderAlertView(&a, s, a.width, contentH)
	case viewDetail:
		content = renderDetail(&a, s, a.width, contentH)
	}

	if a.showHelp {
		content = helpOverlay(a.active, a.width, contentH, &a.theme)
	}

	if a.showServerPicker {
		content = a.renderServerPicker(a.width, contentH)
	}

	if a.sshPrompt != nil {
		content = a.renderSSHPromptModal(a.width, contentH)
	}

	return content + "\n" + a.renderFooter()
}

func (a *App) renderServerPicker(width, height int) string {
	var lines []string
	for i, name := range a.sessionOrder {
		marker := "  "
		if name == a.activeSession {
			marker = "> "
		}
		lines = append(lines, fmt.Sprintf(" %s%d  %s", marker, i+1, name))
	}
	content := ""
	for i, l := range lines {
		if i > 0 {
			content += "\n"
		}
		content += l
	}
	pickerW := 30
	if pickerW > width-4 {
		pickerW = width - 4
	}
	pickerH := len(lines) + 2
	if pickerH > height-2 {
		pickerH = height - 2
	}
	picker := Box("Servers", content, pickerW, pickerH, &a.theme)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, picker)
}

func (a *App) viewHints() string {
	switch a.active {
	case viewDashboard:
		return "Tab Focus  j/k Move  Space Fold  Enter Open  t Track"
	case viewAlerts:
		return "j/k Move  a Ack  s Silence"
	case viewDetail:
		return "j/k Scroll  c Container  g Group  s Stream  / Search"
	}
	return ""
}

func (a *App) renderFooter() string {
	var footer string

	if a.active == viewDetail {
		footer += "  Esc Back"
	}

	type tab struct {
		num    string
		name   string
		target view
	}
	tabs := []tab{
		{"1", "Dashboard", viewDashboard},
		{"2", "Alerts", viewAlerts},
	}
	for _, t := range tabs {
		if t.target == a.active {
			footer += fmt.Sprintf(" [%s %s]", t.num, t.name)
		} else {
			footer += fmt.Sprintf("  %s %s ", t.num, t.name)
		}
	}

	// Show server name when multi-server.
	if len(a.sessions) > 1 {
		if a.active == viewDashboard {
			footer += fmt.Sprintf("  [%s]", a.activeSession)
		} else {
			footer += fmt.Sprintf("  [%s]  S Switch", a.activeSession)
		}
	}

	if hints := a.viewHints(); hints != "" {
		muted := lipgloss.NewStyle().Foreground(a.theme.Muted)
		footer += "  " + muted.Render(hints)
	}

	// Zoom indicator.
	{
		muted := lipgloss.NewStyle().Foreground(a.theme.Muted)
		footer += "  " + muted.Render("+/- Zoom: "+timeWindows[a.windowIdx].label)
	}

	footer += "  ? Help  q Quit"
	return Truncate(footer, a.width)
}

// containerEventToLog converts a container lifecycle event into a synthetic
// log entry. The Stream field is set to "event" so it can be visually
// distinguished from real container logs.
func containerEventToLog(e protocol.ContainerEvent) protocol.LogEntryMsg {
	msg := fmt.Sprintf("── %s %s ──", e.Name, e.Action)
	return protocol.LogEntryMsg{
		Timestamp:   e.Timestamp,
		ContainerID: e.ContainerID,
		Stream:      "event",
		Message:     msg,
	}
}
