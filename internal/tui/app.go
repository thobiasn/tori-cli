package tui

import (
	"context"
	"fmt"
	"sort"
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

// App is the root Bubbletea model.
type App struct {
	sessions      map[string]*Session
	sessionOrder  []string // sorted server names for deterministic iteration
	activeSession string
	width         int
	height        int
	active        view
	theme         Theme
	err           error
	showHelp      bool
	showServerPicker bool
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
	}
}

// subscribeAll subscribes to all streaming topics, queries containers, and
// backfills graph history from the last 30 minutes of stored metrics.
func subscribeAll(c *Client) tea.Cmd {
	return tea.Batch(
		subscribeAndQueryContainers(c),
		backfillMetrics(c),
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

func backfillMetrics(c *Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		now := time.Now().Unix()
		resp, err := c.QueryMetrics(ctx, now-1800, now)
		if err != nil {
			return nil // Non-critical: graphs fill from live data instead.
		}
		return metricsBackfillMsg{server: c.server, resp: resp}
	}
}

type sessionContainersMsg struct {
	server     string
	containers []protocol.ContainerInfo
}
type metricsBackfillMsg struct {
	server string
	resp   *protocol.QueryMetricsResp
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
	for _, s := range a.sessions {
		cmds = append(cmds, subscribeAll(s.Client))
	}
	return tea.Batch(cmds...)
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		return a, nil

	case ConnErrMsg:
		// Only quit on error for single-server mode.
		if len(a.sessions) == 1 {
			a.err = msg.Err
			return a, tea.Quit
		}
		if s := a.sessions[msg.Server]; s != nil {
			s.Err = msg.Err
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
			handleMetricsBackfill(s, msg.resp)
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
			entry := containerEventToLog(msg.ContainerEvent)
			s.Detail.onStreamEntry(entry)
		}
		return a, nil

	case alertActionDoneMsg:
		if s := a.session(); s != nil {
			s.Alertv.stale = true
		}
		return a, nil

	case restartDoneMsg:
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

	case tea.KeyMsg:
		return a.handleKey(msg)
	}
	return a, nil
}

func (a *App) handleSessionMetrics(s *Session, m *protocol.MetricsUpdate) tea.Cmd {
	s.Host = m.Host
	s.Disks = m.Disks
	s.Containers = m.Containers
	s.Rates.Update(m.Timestamp, m.Networks, m.Containers)

	if m.Host != nil {
		s.HostCPUHistory.Push(m.Host.CPUPercent)
		s.HostMemHistory.Push(m.Host.MemPercent)
		pushMemHistories(s, m.Host)
	}

	// Per-container history + stale cleanup.
	current := make(map[string]bool, len(m.Containers))
	for _, c := range m.Containers {
		current[c.ID] = true
		if _, ok := s.CPUHistory[c.ID]; !ok {
			s.CPUHistory[c.ID] = NewRingBuffer[float64](180)
		}
		if _, ok := s.MemHistory[c.ID]; !ok {
			s.MemHistory[c.ID] = NewRingBuffer[float64](180)
		}
		s.CPUHistory[c.ID].Push(c.CPUPercent)
		s.MemHistory[c.ID].Push(float64(c.MemUsage))
	}
	for id := range s.CPUHistory {
		if !current[id] {
			delete(s.CPUHistory, id)
			delete(s.MemHistory, id)
		}
	}

	// Rebuild container groups for dashboard.
	s.Dash.groups = buildGroups(m.Containers, s.ContInfo)
	return nil
}

// handleMetricsBackfill populates ring buffers from historical metrics so
// graphs show data immediately on connect rather than starting empty.
func handleMetricsBackfill(s *Session, resp *protocol.QueryMetricsResp) {
	for _, h := range resp.Host {
		s.HostCPUHistory.Push(h.CPUPercent)
		s.HostMemHistory.Push(h.MemPercent)
		hm := h.HostMetrics
		pushMemHistories(s, &hm)
	}
	for _, c := range resp.Containers {
		if _, ok := s.CPUHistory[c.ID]; !ok {
			s.CPUHistory[c.ID] = NewRingBuffer[float64](180)
		}
		if _, ok := s.MemHistory[c.ID]; !ok {
			s.MemHistory[c.ID] = NewRingBuffer[float64](180)
		}
		s.CPUHistory[c.ID].Push(c.CPUPercent)
		s.MemHistory[c.ID].Push(float64(c.MemUsage))
	}
}

func (a App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Global quit.
	if key == "q" || key == "ctrl+c" {
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
	if key == "S" && len(a.sessions) > 1 {
		a.showServerPicker = true
		return a, nil
	}

	// View switching.
	if cmd, ok := a.handleViewSwitch(key); ok {
		return a, cmd
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

func (a *App) handleServerPicker(key string) (App, tea.Cmd) {
	// Number keys 1-9 select a server.
	if key >= "1" && key <= "9" {
		idx := int(key[0]-'0') - 1
		if idx < len(a.sessionOrder) {
			prev := a.activeSession
			a.activeSession = a.sessionOrder[idx]
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
		// Cycle between dashboard and alerts (skip detail — it's entered via Enter).
		if a.active == viewDashboard {
			a.active = viewAlerts
		} else {
			a.active = viewDashboard
		}
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
	if s == nil {
		return nil
	}
	switch a.active {
	case viewAlerts:
		return s.Alertv.onSwitch(s.Client)
	case viewDetail:
		return s.Detail.onSwitch(s.Client)
	}
	return nil
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
	if s.Err != nil {
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
		return "j/k Move  Space Fold  Enter Open  t Track"
	case viewAlerts:
		return "j/k Move  a Ack  s Silence"
	case viewDetail:
		return "j/k Scroll  c Container  g Group  s Stream  / Search  r Restart"
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
		footer += fmt.Sprintf("  [%s]  S Switch", a.activeSession)
	}

	if hints := a.viewHints(); hints != "" {
		muted := lipgloss.NewStyle().Foreground(a.theme.Muted)
		footer += "  " + muted.Render(hints)
	}

	footer += "  ? Help  q Quit"
	return Truncate(footer, a.width)
}

// pushMemHistories pushes memory usage percentage to the history buffer.
func pushMemHistories(s *Session, h *protocol.HostMetrics) {
	if h.MemTotal == 0 {
		return
	}
	s.HostMemUsedHistory.Push(h.MemPercent)
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
