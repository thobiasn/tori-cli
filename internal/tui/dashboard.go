package tui

import (
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

// renderDashboard assembles the dashboard layout.
func renderDashboard(a *App, width, height int) string {
	theme := &a.theme

	// Alert panel: dynamic height.
	alertH := 3
	if len(a.alerts) > 0 {
		alertH = len(a.alerts) + 2
	}
	maxAlertH := height / 3
	if maxAlertH < 3 {
		maxAlertH = 3
	}
	if alertH > maxAlertH {
		alertH = maxAlertH
	}
	alertPanel := renderAlertPanel(a.alerts, width, theme)

	remaining := height - alertH
	if remaining < 10 {
		remaining = 10
	}

	// Log panel: minimum 5 lines, takes remaining space.
	logH := remaining / 4
	if logH < 5 {
		logH = 5
	}
	maxLogH := remaining - 5
	if maxLogH < 5 {
		maxLogH = 5
	}
	if logH > maxLogH {
		logH = maxLogH
	}

	middleH := remaining - logH
	if middleH < 5 {
		middleH = 5
	}

	// Layout: narrow = stacked, wide = side-by-side.
	var middlePanel string
	if width < 100 {
		// Stacked: containers on top, host below.
		hostH := 10
		contH := middleH - hostH
		if contH < 5 {
			contH = 5
			hostH = middleH - contH
		}
		contPanel := renderContainerPanel(a.dash.groups, a.dash.collapsed, a.dash.cursor, width, contH, theme)
		hostPanel := renderHostPanel(a.host, a.disks, a.rates, width, hostH, theme)
		middlePanel = lipgloss.JoinVertical(lipgloss.Left, contPanel, hostPanel)
	} else {
		// Side-by-side: containers 65%, host 35%.
		contW := width * 65 / 100
		hostW := width - contW
		contPanel := renderContainerPanel(a.dash.groups, a.dash.collapsed, a.dash.cursor, contW, middleH, theme)
		hostPanel := renderHostPanel(a.host, a.disks, a.rates, hostW, middleH, theme)
		middlePanel = lipgloss.JoinHorizontal(lipgloss.Top, contPanel, hostPanel)
	}

	logPanel := renderLogPanel(a.logs, width, logH, theme)

	parts := []string{alertPanel, middlePanel, logPanel}
	return strings.Join(parts, "\n")
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
