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

	alertPanel := renderAlertPanel(s.Alerts, width, theme)

	if width >= 100 {
		// Wide: 4-quadrant layout (side-by-side top and middle).
		leftW := width * 65 / 100
		rightW := width - leftW

		cpuPanel := renderCPUPanel(cpuHistory, s.Host, leftW, cpuH, theme)
		memHist := memHistories{
			Used:      s.HostMemUsedHistory.Data(),
			Available: s.HostMemAvailHistory.Data(),
			Cached:    s.HostMemCachedHistory.Data(),
			Free:      s.HostMemFreeHistory.Data(),
		}
		// Split right column: memory on top, disks on bottom.
		diskH := len(s.Disks)*3 + 2 // 3 lines per disk (divider + used + free) + borders
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

		memPanel := renderMemPanel(s.Host, memHist, rightW, memH, theme)
		diskPanel := renderDiskPanel(s.Disks, rightW, diskH, theme)
		rightCol := lipgloss.JoinVertical(lipgloss.Left, memPanel, diskPanel)
		topRow := lipgloss.JoinHorizontal(lipgloss.Top, cpuPanel, rightCol)

		halfW := width / 2
		midRightW := width - halfW
		contPanel := renderContainerPanel(s.Dash.groups, s.Dash.collapsed, s.Dash.cursor, s.Alerts, s.ContInfo, halfW, middleH, theme)
		selPanel := renderSelectedPanel(a, s, midRightW, middleH, theme)
		midRow := lipgloss.JoinHorizontal(lipgloss.Top, contPanel, selPanel)

		return strings.Join([]string{alertPanel, topRow, midRow}, "\n")
	}

	// Narrow (80-99): stacked layout.
	selH := 8
	memH := 14
	contH := middleH - selH - memH
	if contH < 4 {
		contH = 4
		// Reclaim from selH if needed.
		selH = middleH - memH - contH
		if selH < 4 {
			selH = 4
		}
	}
	cpuPanel := renderCPUPanel(cpuHistory, s.Host, width, cpuH, theme)
	memHist := memHistories{
		Used:      s.HostMemUsedHistory.Data(),
		Available: s.HostMemAvailHistory.Data(),
		Cached:    s.HostMemCachedHistory.Data(),
		Free:      s.HostMemFreeHistory.Data(),
	}
	memPanel := renderMemPanel(s.Host, memHist, width, memH, theme)
	diskH := len(s.Disks)*3 + 2
	if diskH < 3 {
		diskH = 3
	}
	if diskH > 11 {
		diskH = 11
	}
	diskPanel := renderDiskPanel(s.Disks, width, diskH, theme)
	contPanel := renderContainerPanel(s.Dash.groups, s.Dash.collapsed, s.Dash.cursor, s.Alerts, s.ContInfo, width, contH, theme)
	selPanel := renderSelectedPanel(a, s, width, selH, theme)

	return strings.Join([]string{alertPanel, cpuPanel, memPanel, diskPanel, contPanel, selPanel}, "\n")
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
	return true
}
