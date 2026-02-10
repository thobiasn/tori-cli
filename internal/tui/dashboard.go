package tui

import (
	"fmt"
	"sort"
	"strings"

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
	for _, c := range containers {
		proj := projectOf[c.ID]
		if proj == "" {
			proj = "other"
		}
		grouped[proj] = append(grouped[proj], c)
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

// renderDashboard assembles the 4-quadrant dashboard layout.
func renderDashboard(a *App, width, height int) string {
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
	if len(a.alerts) > 0 {
		alertH = len(a.alerts) + 2
		maxAlertH := height / 4
		if maxAlertH < 3 {
			maxAlertH = 3
		}
		if alertH > maxAlertH {
			alertH = maxAlertH
		}
	}

	cpuH := height * 20 / 100
	if cpuH < 6 {
		cpuH = 6
	}

	logH := (height - cpuH - alertH) * 25 / 100
	if logH < 5 {
		logH = 5
	}

	middleH := height - cpuH - alertH - logH
	if middleH < 8 {
		middleH = 8
		// Reclaim from logs if needed.
		logH = height - cpuH - alertH - middleH
		if logH < 5 {
			logH = 5
		}
	}

	cpuHistory := a.hostCPUHistory.Data()

	alertPanel := renderAlertPanel(a.alerts, width, theme)
	logPanel := renderLogPanel(a.logs, width, logH, theme)

	if width >= 100 {
		// Wide: 4-quadrant layout (side-by-side top and middle).
		halfW := width / 2
		rightW := width - halfW

		cpuPanel := renderCPUPanel(cpuHistory, a.host, halfW, cpuH, theme)
		memPanel := renderMemPanel(a.host, rightW, cpuH, theme)
		topRow := lipgloss.JoinHorizontal(lipgloss.Top, cpuPanel, memPanel)

		contPanel := renderContainerPanel(a.dash.groups, a.dash.collapsed, a.dash.cursor, halfW, middleH, theme)
		selPanel := renderSelectedPanel(a, rightW, middleH, theme)
		midRow := lipgloss.JoinHorizontal(lipgloss.Top, contPanel, selPanel)

		return strings.Join([]string{topRow, midRow, alertPanel, logPanel}, "\n")
	}

	// Narrow (80-99): stacked layout.
	cpuPanel := renderCPUPanel(cpuHistory, a.host, width, cpuH, theme)
	memPanel := renderMemPanel(a.host, width, 6, theme)
	contPanel := renderContainerPanel(a.dash.groups, a.dash.collapsed, a.dash.cursor, width, middleH-6, theme)
	selPanel := renderSelectedPanel(a, width, 8, theme)

	return strings.Join([]string{cpuPanel, memPanel, contPanel, selPanel, alertPanel, logPanel}, "\n")
}

// updateDashboard handles keys for the dashboard view.
func updateDashboard(a *App, msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "j", "down":
		max := maxCursorPos(a.dash.groups, a.dash.collapsed)
		if a.dash.cursor < max {
			a.dash.cursor++
		}
	case "k", "up":
		if a.dash.cursor > 0 {
			a.dash.cursor--
		}
	case " ":
		name := cursorGroupName(a.dash.groups, a.dash.collapsed, a.dash.cursor)
		if name != "" {
			a.dash.collapsed[name] = !a.dash.collapsed[name]
		}
	case "enter":
		id := cursorContainerID(a.dash.groups, a.dash.collapsed, a.dash.cursor)
		if id != "" {
			a.detail.containerID = id
			a.detail.reset()
			a.active = viewDetail
			return a.detail.onSwitch(a.client)
		}
	case "l":
		// Jump to log view filtered to selected container.
		id := cursorContainerID(a.dash.groups, a.dash.collapsed, a.dash.cursor)
		if id != "" {
			a.logv.filterContainerID = id
			a.active = viewLogs
			return a.logv.onSwitch(a.client)
		}
	}
	return nil
}
