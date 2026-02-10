package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thobiasn/rook/internal/protocol"
)

type view int

const (
	viewDashboard view = iota
	viewLogs
	viewAlerts
	viewDetail
)

// App is the root Bubbletea model.
type App struct {
	client *Client
	width  int
	height int
	active view
	theme  Theme
	err    error

	// Accumulated live data.
	host       *protocol.HostMetrics
	disks      []protocol.DiskMetrics
	containers []protocol.ContainerMetrics
	contInfo   []protocol.ContainerInfo
	logs       *RingBuffer[protocol.LogEntryMsg]
	alerts     map[int64]*protocol.AlertEvent

	// History buffers.
	rates          *RateCalc
	cpuHistory     map[string]*RingBuffer[float64]
	memHistory     map[string]*RingBuffer[float64]
	hostCPUHistory *RingBuffer[float64]
	hostMemHistory *RingBuffer[float64]

	// Sub-view state.
	dash     DashboardState
	logv     LogViewState
	alertv   AlertViewState
	detail   DetailState
	showHelp bool
}

// NewApp creates the root model.
func NewApp(client *Client) App {
	return App{
		client:         client,
		theme:          DefaultTheme(),
		logs:           NewRingBuffer[protocol.LogEntryMsg](500),
		alerts:         make(map[int64]*protocol.AlertEvent),
		rates:          NewRateCalc(),
		cpuHistory:     make(map[string]*RingBuffer[float64]),
		memHistory:     make(map[string]*RingBuffer[float64]),
		hostCPUHistory: NewRingBuffer[float64](180),
		hostMemHistory: NewRingBuffer[float64](180),
		dash:           newDashboardState(),
		logv:           newLogViewState(),
		alertv:         newAlertViewState(),
	}
}

// subscribeAll subscribes to all streaming topics and queries containers.
func subscribeAll(c *Client) tea.Cmd {
	return func() tea.Msg {
		if err := c.Subscribe(protocol.TypeSubscribeMetrics, nil); err != nil {
			return ConnErrMsg{Err: fmt.Errorf("subscribe metrics: %w", err)}
		}
		if err := c.Subscribe(protocol.TypeSubscribeLogs, nil); err != nil {
			return ConnErrMsg{Err: fmt.Errorf("subscribe logs: %w", err)}
		}
		if err := c.Subscribe(protocol.TypeSubscribeAlerts, nil); err != nil {
			return ConnErrMsg{Err: fmt.Errorf("subscribe alerts: %w", err)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		containers, err := c.QueryContainers(ctx)
		if err != nil {
			return ConnErrMsg{Err: fmt.Errorf("query containers: %w", err)}
		}
		return containersMsg(containers)
	}
}

type containersMsg []protocol.ContainerInfo

func (a App) Init() tea.Cmd {
	return subscribeAll(a.client)
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		return a, nil

	case ConnErrMsg:
		a.err = msg.Err
		return a, tea.Quit

	case containersMsg:
		a.contInfo = []protocol.ContainerInfo(msg)
		return a, nil

	case MetricsMsg:
		return a, a.handleMetrics(msg.MetricsUpdate)

	case LogMsg:
		a.logs.Push(msg.LogEntryMsg)
		if a.active == viewLogs {
			a.logv.onStreamEntry(msg.LogEntryMsg)
		}
		if a.active == viewDetail {
			a.detail.onStreamEntry(msg.LogEntryMsg)
		}
		return a, nil

	case AlertEventMsg:
		if msg.State == "resolved" {
			delete(a.alerts, msg.ID)
		} else {
			e := msg.AlertEvent
			a.alerts[msg.ID] = &e
		}
		return a, nil

	case logQueryMsg:
		a.logv.handleBackfill(msg)
		return a, nil

	case alertQueryMsg:
		a.alertv.alerts = msg.alerts
		a.alertv.stale = false
		return a, nil

	case detailLogQueryMsg:
		a.detail.handleBackfill(msg)
		return a, nil

	case tea.KeyMsg:
		return a.handleKey(msg)
	}
	return a, nil
}

func (a *App) handleMetrics(m *protocol.MetricsUpdate) tea.Cmd {
	a.host = m.Host
	a.disks = m.Disks
	a.containers = m.Containers
	a.rates.Update(m.Timestamp, m.Networks, m.Containers)

	if m.Host != nil {
		a.hostCPUHistory.Push(m.Host.CPUPercent)
		a.hostMemHistory.Push(m.Host.MemPercent)
	}

	// Per-container history + stale cleanup.
	current := make(map[string]bool, len(m.Containers))
	for _, c := range m.Containers {
		current[c.ID] = true
		if _, ok := a.cpuHistory[c.ID]; !ok {
			a.cpuHistory[c.ID] = NewRingBuffer[float64](180)
		}
		if _, ok := a.memHistory[c.ID]; !ok {
			a.memHistory[c.ID] = NewRingBuffer[float64](180)
		}
		a.cpuHistory[c.ID].Push(c.CPUPercent)
		a.memHistory[c.ID].Push(c.MemPercent)
	}
	for id := range a.cpuHistory {
		if !current[id] {
			delete(a.cpuHistory, id)
			delete(a.memHistory, id)
		}
	}

	// Rebuild container groups for dashboard.
	a.dash.groups = buildGroups(m.Containers, a.contInfo)
	return nil
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
		// Any key dismisses help.
		a.showHelp = false
		return a, nil
	}

	// View switching.
	if cmd, ok := a.handleViewSwitch(key); ok {
		return a, cmd
	}

	// Delegate to active view.
	switch a.active {
	case viewDashboard:
		return a, updateDashboard(&a, msg)
	case viewLogs:
		return a, updateLogView(&a, msg)
	case viewAlerts:
		return a, updateAlertView(&a, msg)
	case viewDetail:
		return a, updateDetail(&a, msg)
	}
	return a, nil
}

func (a *App) handleViewSwitch(key string) (tea.Cmd, bool) {
	prev := a.active
	switch key {
	case "tab":
		a.active = (a.active + 1) % 4
	case "1":
		a.active = viewDashboard
	case "2":
		a.active = viewLogs
	case "3":
		a.active = viewAlerts
	case "4":
		a.active = viewDetail
	default:
		return nil, false
	}
	if a.active == prev {
		return nil, true
	}
	return a.onViewSwitch(), true
}

func (a *App) onViewSwitch() tea.Cmd {
	switch a.active {
	case viewLogs:
		return a.logv.onSwitch(a.client)
	case viewAlerts:
		return a.alertv.onSwitch(a.client)
	case viewDetail:
		return a.detail.onSwitch(a.client)
	}
	return nil
}

func (a App) View() string {
	if a.err != nil {
		return fmt.Sprintf("Error: %v\n", a.err)
	}
	if a.width == 0 || a.height == 0 {
		return "Connecting..."
	}

	// Reserve 1 line for footer.
	contentH := a.height - 1
	if contentH < 1 {
		contentH = 1
	}

	var content string
	switch a.active {
	case viewDashboard:
		content = renderDashboard(&a, a.width, contentH)
	case viewLogs:
		content = renderLogView(&a, a.width, contentH)
	case viewAlerts:
		content = renderAlertView(&a, a.width, contentH)
	case viewDetail:
		content = renderDetail(&a, a.width, contentH)
	}

	if a.showHelp {
		content = helpOverlay(a.active, a.width, contentH, &a.theme, content)
	}

	return content + "\n" + a.renderFooter()
}

func (a *App) renderFooter() string {
	tabs := [4]struct {
		num  string
		name string
	}{
		{"1", "Dashboard"},
		{"2", "Logs"},
		{"3", "Alerts"},
		{"4", "Detail"},
	}

	var footer string
	for i, t := range tabs {
		if view(i) == a.active {
			footer += fmt.Sprintf(" [%s %s]", t.num, t.name)
		} else {
			footer += fmt.Sprintf("  %s %s ", t.num, t.name)
		}
	}
	footer += "  ? Help  q Quit"
	return Truncate(footer, a.width)
}
