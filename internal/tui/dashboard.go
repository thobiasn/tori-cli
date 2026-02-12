package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/rook/internal/protocol"
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

	alertPanel := renderAlertPanel(s.Alerts, width, theme)

	// Host box inner dimensions (outer border takes 2 rows/cols).
	hostInnerW := width - 2
	hostInnerH := cpuH - 2

	if width >= 100 {
		// Wide: 4-quadrant layout (side-by-side top and middle).
		leftW := hostInnerW * 65 / 100
		rightW := hostInnerW - leftW

		cpuPanel := renderCPUPanel(cpuHistory, s.Host, leftW, hostInnerH, theme, windowLabel, a.windowSeconds())
		// Split right column: memory on top, disks on bottom.
		diskH := len(s.Disks)*3 + 2 // 3 lines per disk (divider + used + free) + borders
		if s.Host != nil && s.Host.SwapTotal > 0 {
			diskH += 3 // swap: divider + used + free
		}
		if diskH < 3 {
			diskH = 3
		}
		if diskH > hostInnerH/2 {
			diskH = hostInnerH / 2
		}
		memH := hostInnerH - diskH
		if memH < 8 {
			memH = 8
			diskH = hostInnerH - memH
		}

		memPanel := renderMemPanel(s.Host, s.HostMemUsedHistory.Data(), rightW, memH, theme, windowLabel, a.windowSeconds())
		var swapTotal, swapUsed uint64
		if s.Host != nil {
			swapTotal, swapUsed = s.Host.SwapTotal, s.Host.SwapUsed
		}
		diskPanel := renderDiskPanel(s.Disks, swapTotal, swapUsed, rightW, diskH, theme)
		rightCol := lipgloss.JoinVertical(lipgloss.Left, memPanel, diskPanel)
		hostContent := lipgloss.JoinHorizontal(lipgloss.Top, cpuPanel, rightCol)
		hostBox := Box("Host", hostContent, width, cpuH, theme)

		listW := width * 3 / 5
		midRightW := width - listW
		contPanel := renderContainerPanel(s.Dash.groups, s.Dash.collapsed, s.Dash.cursor, s.Alerts, s.ContInfo, listW, middleH, theme)
		selPanel := renderSelectedPanel(a, s, midRightW, middleH, theme)
		midRow := lipgloss.JoinHorizontal(lipgloss.Top, contPanel, selPanel)

		return strings.Join([]string{alertPanel, hostBox, midRow}, "\n")
	}

	// Narrow (80-99): stacked layout.
	// Host box wraps CPU + MEM + Disk stacked vertically.
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
	hostH := cpuH + narrowMemH + diskH + 2 // +2 for host box borders
	cpuPanelH := hostH - 2 - narrowMemH - diskH
	if cpuPanelH < minCpuPanelH {
		cpuPanelH = minCpuPanelH
		hostH = cpuPanelH + narrowMemH + diskH + 2
	}

	remaining := height - alertH - hostH
	selH := 8
	contH := remaining - selH
	if contH < 4 {
		contH = 4
		selH = remaining - contH
		if selH < 4 {
			selH = 4
		}
	}

	var swapTotal2, swapUsed2 uint64
	if s.Host != nil {
		swapTotal2, swapUsed2 = s.Host.SwapTotal, s.Host.SwapUsed
	}
	cpuPanel := renderCPUPanel(cpuHistory, s.Host, hostInnerW, cpuPanelH, theme, windowLabel, a.windowSeconds())
	memPanel := renderMemPanel(s.Host, s.HostMemUsedHistory.Data(), hostInnerW, narrowMemH, theme, windowLabel, a.windowSeconds())
	diskPanel := renderDiskPanel(s.Disks, swapTotal2, swapUsed2, hostInnerW, diskH, theme)
	hostContent := strings.Join([]string{cpuPanel, memPanel, diskPanel}, "\n")
	hostBox := Box("Host", hostContent, width, hostH, theme)
	contPanel := renderContainerPanel(s.Dash.groups, s.Dash.collapsed, s.Dash.cursor, s.Alerts, s.ContInfo, width, contH, theme)
	selPanel := renderSelectedPanel(a, s, width, selH, theme)

	return strings.Join([]string{alertPanel, hostBox, contPanel, selPanel}, "\n")
}

// updateDashboard handles keys for the dashboard view.
func updateDashboard(a *App, s *Session, msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
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
			return s.Detail.onSwitch(s.Client)
		}
		// Enter on group header opens detail in group mode.
		groupName := cursorGroupName(s.Dash.groups, s.Dash.collapsed, s.Dash.cursor)
		if groupName != "" && groupName != "other" {
			s.Detail.containerID = ""
			s.Detail.project = groupName
			s.Detail.reset()
			// Populate projectIDs from contInfo.
			s.Detail.projectIDs = nil
			for _, ci := range s.ContInfo {
				if ci.Project == groupName {
					s.Detail.projectIDs = append(s.Detail.projectIDs, ci.ID)
				}
			}
			a.active = viewDetail
			return s.Detail.onSwitch(s.Client)
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
