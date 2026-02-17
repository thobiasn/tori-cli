package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

type containerGroup struct {
	name       string
	containers []protocol.ContainerMetrics
	running    int
}

// listItem represents a selectable row in the container list.
type listItem struct {
	isProject bool
	groupIdx  int
	contIdx   int // only valid when !isProject
}

// buildSelectableItems builds a flat list of selectable items from groups,
// respecting collapsed state.
func buildSelectableItems(groups []containerGroup, collapsed map[string]bool) []listItem {
	var items []listItem
	for gi, g := range groups {
		items = append(items, listItem{isProject: true, groupIdx: gi})
		if !collapsed[g.name] {
			for ci := range g.containers {
				items = append(items, listItem{groupIdx: gi, contIdx: ci})
			}
		}
	}
	return items
}

// buildGroups groups containers by compose project. "other" is for unlabeled.
func buildGroups(containers []protocol.ContainerMetrics, contInfo []protocol.ContainerInfo) []containerGroup {
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
			proj = c.Project
		}
		if proj == "" {
			proj = "other"
		}
		grouped[proj] = append(grouped[proj], c)
	}

	// Inject untracked containers from contInfo as stubs.
	for _, ci := range contInfo {
		if seen[ci.ID] {
			continue
		}
		stub := protocol.ContainerMetrics{
			ID: ci.ID, Name: ci.Name, Image: ci.Image,
			State: ci.State, Health: ci.Health,
			Project: ci.Project, Service: ci.Service,
			StartedAt: ci.StartedAt, RestartCount: ci.RestartCount,
			ExitCode: ci.ExitCode,
		}
		proj := ci.Project
		if proj == "" {
			proj = "other"
		}
		grouped[proj] = append(grouped[proj], stub)
	}

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
		sort.Slice(conts, func(i, j int) bool {
			ni, nj := conts[i].Service, conts[j].Service
			if ni == "" {
				ni = conts[i].Name
			}
			if nj == "" {
				nj = conts[j].Name
			}
			return ni < nj
		})
		groups = append(groups, containerGroup{name: name, containers: conts, running: running})
	}
	return groups
}

const maxContentW = 80

// renderDashboard assembles the full dashboard view.
func renderDashboard(a *App, s *Session, width, height int) string {
	theme := &a.theme

	// Content width: capped at maxContentW, centered.
	contentW := width
	if contentW > maxContentW {
		contentW = maxContentW
	}

	var sections []string

	// 1. Header: logo + server name + status
	sections = append(sections, renderHeader(a, s, contentW, theme))

	// 2. Divider with time window label
	sections = append(sections, renderLabeledDivider(a.windowLabel(), contentW, theme))

	// 3. Host metrics graphs (cpu + mem braille, 2 rows each)
	sections = append(sections, renderHostGraphs(a, s, contentW, theme))

	// 4. Disk + load summary line
	summaryLine := 1
	if s.Host != nil {
		muted := mutedStyle(theme)
		var parts []string
		if len(s.Disks) > 0 {
			var maxPct float64
			for _, d := range s.Disks {
				if d.Percent > maxPct {
					maxPct = d.Percent
				}
			}
			diskColor := diskSeverityColor(maxPct, theme)
			parts = append(parts,
				muted.Render("disk ")+lipgloss.NewStyle().Foreground(diskColor).Render(fmt.Sprintf("%.1f%%", maxPct)))
		}
		loadColor := loadSeverityColor(s.Host.Load1, s.Host.CPUs, theme)
		loadVals := fmt.Sprintf("%.2f %.2f %.2f", s.Host.Load1, s.Host.Load5, s.Host.Load15)
		parts = append(parts,
			muted.Render("load ")+lipgloss.NewStyle().Foreground(loadColor).Render(loadVals))
		sections = append(sections, centerText(strings.Join(parts, styledSep(theme)), contentW))
	} else {
		sections = append(sections, centerText(mutedStyle(theme).Render("disk —  ·  load — — —"), contentW))
	}

	// 5. Divider
	sections = append(sections, renderDivider(contentW, theme))

	// 6. Container list (fills remaining space)
	// Fixed sections: header(3) + time divider(2) + host graphs(4) + divider(1) + divider(1) + status(1) + help(1) = 13
	fixedH := 13 + summaryLine
	contH := height - fixedH
	if contH < 1 {
		contH = 1
	}
	sections = append(sections, renderContainerList(a, s, contentW, contH, theme))

	// 5. Divider
	sections = append(sections, renderDivider(contentW, theme))

	// 6. Status line
	sections = append(sections, renderStatusLine(s, contentW, theme))

	// 7. Help bar
	sections = append(sections, dashboardHelpBar(contentW, theme))

	return pageFrame(strings.Join(sections, "\n"), contentW, width, height)
}

func renderHeader(a *App, s *Session, w int, theme *Theme) string {
	muted := mutedStyle(theme)

	var logo string
	if s.Host == nil {
		logo = accentStyle(theme).Render(strings.TrimLeft(birdFrames[a.spinnerFrame%len(birdFrames)], " "))
	} else {
		logo = birdIcon(a.birdBlink, theme)
	}

	// No connection attempted yet — bird + status only, no server name.
	if s.ConnState == ConnNone {
		return centerText(logo, w) + "\n\n" + centerText(muted.Render("not connected"), w)
	}

	// Server name + health status + alert summary.
	var statusStr, alertStr string
	switch s.ConnState {
	case ConnReady:
		statusStr = lipgloss.NewStyle().Foreground(theme.Healthy).Render("healthy")
		if n := len(s.Alerts); n > 0 {
			alertStr = lipgloss.NewStyle().Foreground(theme.Critical).Render(fmt.Sprintf("%d alert(s)", n))
		} else {
			alertStr = lipgloss.NewStyle().Foreground(theme.Healthy).Render("✓ all clear")
		}
	case ConnConnecting, ConnSSH:
		statusStr = muted.Render("connecting…")
	case ConnError:
		statusStr = lipgloss.NewStyle().Foreground(theme.Critical).Render("error")
	default:
		statusStr = muted.Render("disconnected")
	}

	sep := styledSep(theme)
	nameBold := lipgloss.NewStyle().Bold(true).Render(s.Name)
	infoLine := nameBold + sep + statusStr
	if alertStr != "" {
		infoLine += sep + alertStr
	}

	return centerText(logo, w) + "\n\n" + centerText(infoLine, w)
}

// renderHostGraphs renders CPU and memory as 2-row braille sparklines.
// When host data hasn't arrived yet, renders animated loading waves.
func renderHostGraphs(a *App, s *Session, w int, theme *Theme) string {
	muted := mutedStyle(theme)

	// "cpu " / "mem " = 4 chars label, " XX.X%" = 7 chars max suffix.
	labelW := 4
	pctW := 7
	graphW := w - labelW - pctW
	if graphW < 5 {
		graphW = 5
	}
	indent := strings.Repeat(" ", labelW)
	pctPad := strings.Repeat(" ", pctW)

	if s.Host == nil || s.BackfillPending {
		cpuTop, cpuBot := LoadingSparkline(a.spinnerFrame, graphW, theme.FgDim)
		memTop, memBot := LoadingSparkline(a.spinnerFrame+3, graphW, theme.FgDim)
		cpuPct := pctPad
		memPct := pctPad
		if s.Host != nil {
			cpuPct = muted.Render(rightAlign(fmt.Sprintf(" %.1f%%", s.Host.CPUPercent), pctW))
			memPct = muted.Render(rightAlign(fmt.Sprintf(" %.1f%%", s.Host.MemPercent), pctW))
		}
		return indent + cpuTop + pctPad + "\n" +
			muted.Render("cpu ") + cpuBot + cpuPct + "\n" +
			indent + memTop + pctPad + "\n" +
			muted.Render("mem ") + memBot + memPct
	}

	cpuTop, cpuBot := Sparkline(s.HostCPUHist.Data(), graphW, theme.GraphCPU)
	cpuPct := rightAlign(fmt.Sprintf(" %.1f%%", s.Host.CPUPercent), pctW)

	memTop, memBot := Sparkline(s.HostMemHist.Data(), graphW, theme.GraphMem)
	memPct := rightAlign(fmt.Sprintf(" %.1f%%", s.Host.MemPercent), pctW)

	return indent + cpuTop + pctPad + "\n" +
		muted.Render("cpu ") + cpuBot + muted.Render(cpuPct) + "\n" +
		indent + memTop + pctPad + "\n" +
		muted.Render("mem ") + memBot + muted.Render(memPct)
}

func renderStatusLine(s *Session, w int, theme *Theme) string {
	muted := mutedStyle(theme)
	sep := muted.Render(" · ")

	var parts []string
	contCount := len(s.Containers) + countUntrackedStubs(s)
	parts = append(parts, muted.Render(fmt.Sprintf("%d containers", contCount)))

	if s.RuleCount > 0 {
		parts = append(parts, muted.Render(fmt.Sprintf("%d rules active", s.RuleCount)))
	}

	if s.Host != nil && s.Host.Uptime > 0 {
		parts = append(parts, muted.Render(FormatUptime(s.Host.Uptime)+" uptime"))
	}

	line := strings.Join(parts, sep)
	return centerText(line, w)
}

// countUntrackedStubs counts ContInfo entries not present in Containers.
func countUntrackedStubs(s *Session) int {
	seen := make(map[string]bool, len(s.Containers))
	for _, c := range s.Containers {
		seen[c.ID] = true
	}
	count := 0
	for _, ci := range s.ContInfo {
		if !seen[ci.ID] {
			count++
		}
	}
	return count
}

func dashboardHelpBar(w int, theme *Theme) string {
	return renderHelpBar([]helpBinding{
		{"j/k", "navigate"},
		{"enter", "detail"},
		{"space", "expand"},
		{"2", "alerts"},
		{"?", "help"},
		{"q", "quit"},
	}, w, theme)
}

// containerCount returns the total number of containers across all groups.
func containerCount(groups []containerGroup) int {
	n := 0
	for _, g := range groups {
		n += len(g.containers)
	}
	return n
}

// containerAtCursor returns the container metrics at the cursor position,
// or nil if the cursor is on a project row.
func containerAtCursor(groups []containerGroup, items []listItem, cursor int) *protocol.ContainerMetrics {
	if cursor < 0 || cursor >= len(items) {
		return nil
	}
	item := items[cursor]
	if item.isProject {
		return nil
	}
	return &groups[item.groupIdx].containers[item.contIdx]
}

// groupAtCursor returns the group name at the cursor position.
func groupAtCursor(groups []containerGroup, items []listItem, cursor int) string {
	if cursor < 0 || cursor >= len(items) {
		return ""
	}
	return groups[items[cursor].groupIdx].name
}
