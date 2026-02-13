package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

type dashFocus int

const (
	focusServers    dashFocus = iota // default: servers focused on open
	focusContainers
)

// DashboardState holds dashboard-specific state.
type DashboardState struct {
	cursor    int
	collapsed map[string]bool
	groups    []containerGroup
}

type containerGroup struct {
	name       string
	containers []protocol.ContainerMetrics
	running    int
}

func newDashboardState() DashboardState {
	return DashboardState{
		collapsed: make(map[string]bool),
	}
}

// buildGroups groups containers by compose project. "other" is for unlabeled.
func buildGroups(containers []protocol.ContainerMetrics, contInfo []protocol.ContainerInfo) []containerGroup {
	// Build project lookup from contInfo.
	projectOf := make(map[string]string, len(contInfo))
	for _, ci := range contInfo {
		projectOf[ci.ID] = ci.Project
	}

	grouped := make(map[string][]protocol.ContainerMetrics)
	seen := make(map[string]bool, len(containers))
	for _, c := range containers {
		seen[c.ID] = true
		proj := projectOf[c.ID]
		if proj == "" && c.Project != "" {
			proj = c.Project // fallback to service identity from metrics
		}
		if proj == "" {
			proj = "other"
		}
		grouped[proj] = append(grouped[proj], c)
	}

	// Inject untracked containers from contInfo as stubs (zero stats).
	for _, ci := range contInfo {
		if seen[ci.ID] {
			continue
		}
		stub := protocol.ContainerMetrics{
			ID: ci.ID, Name: ci.Name, Image: ci.Image,
			State: ci.State, Health: ci.Health,
			StartedAt: ci.StartedAt, RestartCount: ci.RestartCount,
			ExitCode: ci.ExitCode,
		}
		proj := ci.Project
		if proj == "" {
			proj = "other"
		}
		grouped[proj] = append(grouped[proj], stub)
	}

	// Sort group names alpha, "other" last.
	names := make([]string, 0, len(grouped))
	for n := range grouped {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == "other" {
			return false
		}
		if names[j] == "other" {
			return true
		}
		return names[i] < names[j]
	})

	groups := make([]containerGroup, 0, len(names))
	for _, name := range names {
		conts := grouped[name]
		running := 0
		for _, c := range conts {
			if c.State == "running" {
				running++
			}
		}
		// Sort containers within group by name.
		sort.Slice(conts, func(i, j int) bool {
			return conts[i].Name < conts[j].Name
		})
		groups = append(groups, containerGroup{name: name, containers: conts, running: running})
	}
	return groups
}

// renderDashboard assembles the dashboard layout.
func renderDashboard(a *App, s *Session, width, height int) string {
	theme := &a.theme

	// Minimum size checks.
	if width < 80 {
		return fmt.Sprintf("\n  Terminal too narrow (need 80+ columns, have %d)", width)
	}
	if height < 24 {
		return fmt.Sprintf("\n  Terminal too short (need 24+ rows, have %d)", height)
	}

	// Height calculations.
	alertH := 3
	if len(s.Alerts) > 0 {
		alertH = len(s.Alerts) + 2
		maxAlertH := height / 4
		if maxAlertH < 3 {
			maxAlertH = 3
		}
		if alertH > maxAlertH {
			alertH = maxAlertH
		}
	}

	cpuH := height * 28 / 100
	if cpuH < 12 {
		cpuH = 12
	}

	middleH := height - cpuH - alertH
	if middleH < 8 {
		middleH = 8
	}

	cpuHistory := s.HostCPUHistory.Data()
	windowLabel := a.windowLabel()

	if width >= 100 {
		// Wide: server panel (fixed 20) spans alert+host rows on the left.
		srvW := 32
		hostW := width - srvW
		leftW := hostW * 65 / 100
		rightW := hostW - leftW

		alertPanel := renderAlertPanel(s.Alerts, hostW, theme, a.tsFormat())

		cpuPanel := renderCPUPanel(cpuHistory, s.Host, RenderContext{Width: leftW, Height: cpuH, Theme: theme, WindowLabel: windowLabel, WindowSec: a.windowSeconds()})
		// Split right column: memory on top, disks on bottom.
		diskH := len(s.Disks)*3 + 2 // 3 lines per disk (divider + used + free) + borders
		if s.Host != nil && s.Host.SwapTotal > 0 {
			diskH += 3 // swap: divider + used + free
		}
		if diskH < 3 {
			diskH = 3
		}
		if diskH > cpuH/2 {
			diskH = cpuH / 2
		}
		memH := cpuH - diskH
		if memH < 8 {
			memH = 8
			diskH = cpuH - memH
		}

		memPanel := renderMemPanel(s.Host, s.HostMemUsedHistory.Data(), RenderContext{Width: rightW, Height: memH, Theme: theme, WindowLabel: windowLabel, WindowSec: a.windowSeconds()})
		var swapTotal, swapUsed uint64
		if s.Host != nil {
			swapTotal, swapUsed = s.Host.SwapTotal, s.Host.SwapUsed
		}
		diskPanel := renderDiskPanel(s.Disks, swapTotal, swapUsed, rightW, diskH, theme)
		rightCol := lipgloss.JoinVertical(lipgloss.Left, memPanel, diskPanel)
		hostRow := lipgloss.JoinHorizontal(lipgloss.Top, cpuPanel, rightCol)

		rightContent := lipgloss.JoinVertical(lipgloss.Left, alertPanel, hostRow)
		serverPanel := renderServerPanel(a, alertH+cpuH, srvW)
		topRow := lipgloss.JoinHorizontal(lipgloss.Top, serverPanel, rightContent)

		contPanel := renderContainerPanel(s.Dash.groups, s.Dash.collapsed, s.Dash.cursor, s.Alerts, s.ContInfo, RenderContext{Width: width, Height: middleH, Theme: theme}, a.dashFocus == focusContainers)

		return strings.Join([]string{topRow, contPanel}, "\n")
	}

	alertPanel := renderAlertPanel(s.Alerts, width, theme, a.tsFormat())

	// Narrow (80-99): stacked layout with server panel.
	// 2 lines per server (name + status) + dividers between + borders.
	n := len(a.sessionOrder)
	srvH := n*2 + 2
	if n > 1 {
		srvH += n - 1 // dividers between servers
	}
	serverPanel := renderServerPanel(a, srvH, width)

	diskH := len(s.Disks)*3 + 2
	if s.Host != nil && s.Host.SwapTotal > 0 {
		diskH += 3
	}
	if diskH < 3 {
		diskH = 3
	}
	if diskH > 14 {
		diskH = 14
	}
	narrowMemH := 8
	minCpuPanelH := 8
	hostH := cpuH + narrowMemH + diskH
	cpuPanelH := hostH - narrowMemH - diskH
	if cpuPanelH < minCpuPanelH {
		cpuPanelH = minCpuPanelH
		hostH = cpuPanelH + narrowMemH + diskH
	}

	contH := height - alertH - srvH - hostH
	if contH < 4 {
		contH = 4
	}

	var swapTotal2, swapUsed2 uint64
	if s.Host != nil {
		swapTotal2, swapUsed2 = s.Host.SwapTotal, s.Host.SwapUsed
	}
	cpuPanel := renderCPUPanel(cpuHistory, s.Host, RenderContext{Width: width, Height: cpuPanelH, Theme: theme, WindowLabel: windowLabel, WindowSec: a.windowSeconds()})
	memPanel := renderMemPanel(s.Host, s.HostMemUsedHistory.Data(), RenderContext{Width: width, Height: narrowMemH, Theme: theme, WindowLabel: windowLabel, WindowSec: a.windowSeconds()})
	diskPanel := renderDiskPanel(s.Disks, swapTotal2, swapUsed2, width, diskH, theme)
	contPanel := renderContainerPanel(s.Dash.groups, s.Dash.collapsed, s.Dash.cursor, s.Alerts, s.ContInfo, RenderContext{Width: width, Height: contH, Theme: theme}, a.dashFocus == focusContainers)

	return strings.Join([]string{alertPanel, serverPanel, cpuPanel, memPanel, diskPanel, contPanel}, "\n")
}

// renderServerPanel renders the server list panel.
func renderServerPanel(a *App, height, width int) string {
	theme := &a.theme
	innerW := width - 2
	muted := lipgloss.NewStyle().Foreground(theme.Muted)

	var lines []string
	for i, name := range a.sessionOrder {
		sess := a.sessions[name]

		// Divider between servers.
		if i > 0 {
			div := muted.Render(strings.Repeat("─", innerW-2))
			lines = append(lines, " "+div)
		}

		// Status indicator color based on connection state.
		var indicatorColor lipgloss.Color
		switch sess.ConnState {
		case ConnReady:
			indicatorColor = theme.Healthy
		case ConnConnecting, ConnSSH:
			indicatorColor = theme.Warning
		case ConnError:
			indicatorColor = theme.Critical
		default:
			indicatorColor = theme.Muted
		}
		indicator := lipgloss.NewStyle().Foreground(indicatorColor).Render("●")

		isCursor := a.dashFocus == focusServers && i == a.serverCursor
		isActive := a.dashFocus == focusContainers && name == a.activeSession

		// Name line: "● servername" — wrap if needed.
		namePrefix := fmt.Sprintf(" %s ", indicator)
		nameW := innerW - 3 // "● " prefix = 3 chars
		for j, chunk := range wrapText(name, nameW) {
			var line string
			if j == 0 {
				line = namePrefix + chunk
			} else {
				line = "   " + chunk
			}
			if isCursor {
				line = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(line), innerW))
			} else if isActive {
				line = lipgloss.NewStyle().Foreground(theme.Accent).Render(Truncate(stripANSI(line), innerW))
			}
			lines = append(lines, TruncateStyled(line, innerW))
		}

		// Status message — wrap if needed.
		statusMsg := sess.ConnMsg
		if statusMsg == "" {
			statusMsg = "not connected"
		}
		statusW := innerW - 3 // 3-char indent
		for _, chunk := range wrapText(statusMsg, statusW) {
			line := "   " + muted.Render(chunk)
			if isCursor {
				line = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(line), innerW))
			}
			lines = append(lines, TruncateStyled(line, innerW))
		}
	}

	return Box("Servers", strings.Join(lines, "\n"), width, height, theme, a.dashFocus == focusServers)
}

// updateDashboard handles keys for the dashboard view.
func updateDashboard(a *App, s *Session, msg tea.KeyMsg) tea.Cmd {
	key := msg.String()

	if key == "tab" {
		if a.dashFocus == focusContainers {
			a.dashFocus = focusServers
		} else {
			a.dashFocus = focusContainers
		}
		return nil
	}

	if a.dashFocus == focusServers {
		return updateServerFocus(a, key)
	}

	switch key {
	case "j", "down":
		max := maxCursorPos(s.Dash.groups, s.Dash.collapsed)
		if s.Dash.cursor < max {
			s.Dash.cursor++
		}
	case "k", "up":
		if s.Dash.cursor > 0 {
			s.Dash.cursor--
		}
	case " ":
		name := cursorGroupName(s.Dash.groups, s.Dash.collapsed, s.Dash.cursor)
		if name != "" {
			s.Dash.collapsed[name] = !s.Dash.collapsed[name]
		}
	case "enter":
		if s.Client == nil {
			return nil
		}
		id := cursorContainerID(s.Dash.groups, s.Dash.collapsed, s.Dash.cursor)
		if id != "" {
			s.Detail.containerID = id
			s.Detail.project = ""
			// Set service identity for cross-container history queries.
			s.Detail.svcProject = ""
			s.Detail.svcService = ""
			for _, ci := range s.ContInfo {
				if ci.ID == id {
					if ci.Project != "" && ci.Service != "" {
						s.Detail.svcProject = ci.Project
						s.Detail.svcService = ci.Service
					} else if ci.Name != "" {
						s.Detail.svcService = ci.Name
					}
					break
				}
			}
			s.Detail.reset()
			a.active = viewDetail
			return s.Detail.onSwitch(s.Client, a.windowSeconds(), s.RetentionDays)
		}
		// Enter on group header opens detail in group mode.
		groupName := cursorGroupName(s.Dash.groups, s.Dash.collapsed, s.Dash.cursor)
		if groupName != "" && groupName != "other" {
			s.Detail.containerID = ""
			s.Detail.project = groupName
			s.Detail.svcProject = ""
			s.Detail.svcService = ""
			s.Detail.reset()
			// Populate projectIDs from contInfo.
			s.Detail.projectIDs = nil
			for _, ci := range s.ContInfo {
				if ci.Project == groupName {
					s.Detail.projectIDs = append(s.Detail.projectIDs, ci.ID)
				}
			}
			a.active = viewDetail
			return s.Detail.onSwitch(s.Client, a.windowSeconds(), s.RetentionDays)
		}
	case "t":
		return toggleTracking(s)
	}
	return nil
}

// toggleTracking toggles tracking for the container or group at the cursor.
func toggleTracking(s *Session) tea.Cmd {
	if s.Client == nil {
		return nil
	}
	groupName := cursorGroupName(s.Dash.groups, s.Dash.collapsed, s.Dash.cursor)
	if groupName != "" && groupName != "other" {
		// Toggle project tracking.
		tracked := isProjectTracked(groupName, s.ContInfo)
		client := s.Client
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client.SetTracking(ctx, "", groupName, !tracked)
			return trackingDoneMsg{server: client.server}
		}
	}
	id := cursorContainerID(s.Dash.groups, s.Dash.collapsed, s.Dash.cursor)
	if id != "" {
		name := containerNameByID(id, s.ContInfo)
		tracked := isContainerTracked(id, s.ContInfo)
		client := s.Client
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client.SetTracking(ctx, name, "", !tracked)
			return trackingDoneMsg{server: client.server}
		}
	}
	return nil
}

// updateServerFocus handles keys when the server panel has focus.
func updateServerFocus(a *App, key string) tea.Cmd {
	switch key {
	case "j", "down":
		if a.serverCursor < len(a.sessionOrder)-1 {
			a.serverCursor++
			a.activeSession = a.sessionOrder[a.serverCursor]
			if s := a.session(); s != nil && s.Client != nil {
				return backfillMetrics(s.Client, a.windowSeconds())
			}
		}
	case "k", "up":
		if a.serverCursor > 0 {
			a.serverCursor--
			a.activeSession = a.sessionOrder[a.serverCursor]
			if s := a.session(); s != nil && s.Client != nil {
				return backfillMetrics(s.Client, a.windowSeconds())
			}
		}
	case "enter":
		s := a.sessions[a.sessionOrder[a.serverCursor]]
		if s == nil {
			break
		}
		switch s.ConnState {
		case ConnNone, ConnError:
			s.ConnState = ConnNone
			s.Err = nil
			return func() tea.Msg { return connectServerMsg{name: s.Name} }
		case ConnReady:
			a.dashFocus = focusContainers
		}
	}
	return nil
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
