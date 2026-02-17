package tui2

import (
	"fmt"
	"sort"
	"strings"
	"time"

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
		muted := lipgloss.NewStyle().Foreground(theme.FgDim)
		var parts []string
		if len(s.Disks) > 0 {
			// Use the highest disk usage percentage.
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
		sep := " " + muted.Render("·") + " "
		sections = append(sections, centerText(strings.Join(parts, sep), contentW))
	} else {
		muted := lipgloss.NewStyle().Foreground(theme.FgDim)
		sections = append(sections, centerText(muted.Render("disk —  ·  load — — —"), contentW))
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
	sections = append(sections, renderHelpBar(a, contentW, theme))

	content := strings.Join(sections, "\n")

	// Center the content block in the terminal.
	if width > contentW {
		padLeft := (width - contentW) / 2
		padding := strings.Repeat(" ", padLeft)
		var centered []string
		for _, line := range strings.Split(content, "\n") {
			centered = append(centered, padding+line)
		}
		content = strings.Join(centered, "\n")
	}

	// Pad to full height.
	lines := strings.Split(content, "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}

	return strings.Join(lines, "\n")
}

func renderHeader(a *App, s *Session, w int, theme *Theme) string {
	accent := lipgloss.NewStyle().Foreground(theme.Accent)
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

	var bird string
	if s.Host == nil {
		bird = strings.TrimLeft(birdFrames[a.spinnerFrame%len(birdFrames)], " ")
	} else if a.birdBlink {
		bird = "—(-)>"
	} else {
		bird = "—(•)>"
	}
	logo := accent.Render(bird)

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

	sep := " " + muted.Render("·") + " "
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
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

	// "cpu " / "mem " = 4 chars label, " XX.X%" = 7 chars max suffix.
	labelW := 4
	pctW := 7
	graphW := w - labelW - pctW
	if graphW < 5 {
		graphW = 5
	}
	indent := strings.Repeat(" ", labelW)
	pctPad := strings.Repeat(" ", pctW)

	if s.Host == nil {
		cpuTop, cpuBot := LoadingSparkline(a.spinnerFrame, graphW, theme.FgDim)
		memTop, memBot := LoadingSparkline(a.spinnerFrame+3, graphW, theme.FgDim)
		return indent + cpuTop + pctPad + "\n" +
			muted.Render("cpu ") + cpuBot + pctPad + "\n" +
			indent + memTop + pctPad + "\n" +
			muted.Render("mem ") + memBot + pctPad
	}

	cpuTop, cpuBot := Sparkline(s.HostCPUHist.Data(), graphW, theme.GraphCPU)
	cpuPct := fmt.Sprintf(" %.1f%%", s.Host.CPUPercent)
	for len(cpuPct) < pctW {
		cpuPct = " " + cpuPct
	}

	memTop, memBot := Sparkline(s.HostMemHist.Data(), graphW, theme.GraphMem)
	memPct := fmt.Sprintf(" %.1f%%", s.Host.MemPercent)
	for len(memPct) < pctW {
		memPct = " " + memPct
	}

	return indent + cpuTop + pctPad + "\n" +
		muted.Render("cpu ") + cpuBot + muted.Render(cpuPct) + "\n" +
		indent + memTop + pctPad + "\n" +
		muted.Render("mem ") + memBot + muted.Render(memPct)
}


func renderDivider(w int, theme *Theme) string {
	style := lipgloss.NewStyle().Foreground(theme.Border)
	return centerText(style.Render(strings.Repeat("─", w)), w)
}

func renderSpacedDivider(w int, theme *Theme) string {
	return "\n" + renderDivider(w, theme)
}

func renderLabeledDivider(label string, w int, theme *Theme) string {
	divStyle := lipgloss.NewStyle().Foreground(theme.Border)
	lblStyle := lipgloss.NewStyle().Foreground(theme.FgDim)

	lbl := " " + label + " "
	lblLen := len(lbl)
	side := (w - lblLen) / 2
	var line string
	if side < 1 {
		line = divStyle.Render(strings.Repeat("─", w))
	} else {
		right := w - side - lblLen
		line = divStyle.Render(strings.Repeat("─", side)) + lblStyle.Render(lbl) + divStyle.Render(strings.Repeat("─", right))
	}
	return "\n" + line
}

func renderContainerList(a *App, s *Session, w, maxH int, theme *Theme) string {
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

	items := buildSelectableItems(a.groups, a.collapsed)
	if len(items) == 0 {
		lines := make([]string, maxH)
		if maxH > 0 {
			lines[maxH/2] = centerText(muted.Render("no containers"), w)
		}
		return strings.Join(lines, "\n")
	}

	// Build tracked state lookup.
	trackedState := make(map[string]bool, len(s.ContInfo))
	for _, ci := range s.ContInfo {
		trackedState[ci.ID] = ci.Tracked
	}

	// Build metrics availability lookup (containers with real metrics data).
	metricsAvail := make(map[string]bool, len(s.Containers))
	for _, c := range s.Containers {
		metricsAvail[c.ID] = true
	}

	// Column widths (right-aligned, fixed).
	const cpuW = 6  // " 0.6%" or "13.0%"
	const memW = 8  // "  30.9M" or " 710.1M"
	const hchkW = 3 // "  ✓" or "  ~" or "  ✗"
	const statW = 5 // "  1/1" or "   5d"
	const colsW = cpuW + memW + hchkW + statW
	const minGap = 4 // minimum gap between name and columns

	// Max name widths: total - prefix - columns - gap.
	// Project row prefix: "▾ " = 2 chars.
	projNameMax := w - 2 - colsW - minGap
	if projNameMax < 8 {
		projNameMax = 8
	}
	// Container row prefix: "  ● " = 4 chars.
	contNameMax := w - 4 - colsW - minGap
	if contNameMax < 8 {
		contNameMax = 8
	}

	now := time.Now().Unix()

	var lines []string
	for idx, item := range items {
		g := a.groups[item.groupIdx]

		if item.isProject {
			// Blank line before this group only if the previous group was expanded.
			if item.groupIdx > 0 && !a.collapsed[a.groups[item.groupIdx-1].name] {
				lines = append(lines, "")
			}

			// Chevron.
			chevron := "▾"
			if a.collapsed[g.name] {
				chevron = "▸"
			}

			name := g.name

			// Aggregate metrics.
			var cpuSum float64
			var memSum uint64
			allTracked := true
			anyMetrics := false
			worstCPUColor := theme.FgDim
			worstMemColor := theme.FgDim
			hasCheck := false
			worstHealth := "healthy"
			for _, c := range g.containers {
				// Health tracking includes all containers.
				if hasHealthcheck(c.Health) {
					hasCheck = true
					if c.Health == "unhealthy" {
						worstHealth = "unhealthy"
					} else if c.Health != "healthy" && worstHealth != "unhealthy" {
						worstHealth = c.Health
					}
				}
				tracked := true
				if t, ok := trackedState[c.ID]; ok {
					tracked = t
				}
				if !tracked {
					allTracked = false
					continue
				}
				if c.State == "running" {
					if !metricsAvail[c.ID] {
						continue // stub — no metrics yet
					}
					anyMetrics = true
					cpuSum += c.CPUPercent
					memSum += c.MemUsage
					cc := containerCPUColor(c.CPUPercent, c.CPULimit, theme)
					if colorRank(cc, theme) > colorRank(worstCPUColor, theme) {
						worstCPUColor = cc
					}
					mc := containerMemColor(c.MemPercent, c.MemLimit, theme)
					if colorRank(mc, theme) > colorRank(worstMemColor, theme) {
						worstMemColor = mc
					}
				}
			}

			// CPU column.
			var cpuStr string
			if (!allTracked || !anyMetrics) && cpuSum == 0 {
				cpuStr = "—"
			} else {
				cpuStr = fmt.Sprintf("%.1f%%", cpuSum)
			}
			for len([]rune(cpuStr)) < cpuW {
				cpuStr = " " + cpuStr
			}

			// MEM column.
			var memStr string
			if (!allTracked || !anyMetrics) && memSum == 0 {
				memStr = "—"
			} else {
				memStr = formatBytes(memSum)
			}
			for len([]rune(memStr)) < memW {
				memStr = " " + memStr
			}

			// Health column (worst across children).
			hchkHealth := worstHealth
			if !hasCheck {
				hchkHealth = ""
			}
			styledHchk := "  " + healthIcon(hchkHealth, theme)

			// Running count column.
			statStr := fmt.Sprintf("%d/%d", g.running, len(g.containers))
			for len(statStr) < statW {
				statStr = " " + statStr
			}

			// Color the columns — worst severity from children.
			styledCPU := lipgloss.NewStyle().Foreground(worstCPUColor).Render(cpuStr)
			styledMem := lipgloss.NewStyle().Foreground(worstMemColor).Render(memStr)

			statColor := projectStatColor(g, theme)
			styledStat := lipgloss.NewStyle().Foreground(statColor).Render(statStr)

			// Build project header row.
			chevronStyled := muted.Render(chevron)
			name = Truncate(name, projNameMax)
			nameStyled := lipgloss.NewStyle().Foreground(theme.Fg).Bold(true).Render(name)

			prefix := chevronStyled + " " + nameStyled
			prefixW := lipgloss.Width(prefix)
			gap := w - prefixW - colsW
			if gap < 1 {
				gap = 1
			}

			row := prefix + strings.Repeat(" ", gap) + styledCPU + styledMem + styledHchk + styledStat
			if idx == a.cursor {
				row = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(row), w))
			}
			lines = append(lines, TruncateStyled(row, w))
		} else {
			// Container row.
			c := g.containers[item.contIdx]
			tracked := true
			if t, ok := trackedState[c.ID]; ok {
				tracked = t
			}

			// State dot (healthcheck-aware).
			dot := lipgloss.NewStyle().Foreground(theme.StatusDotColor(c.State, c.Health)).Render("●")

			// Prefer compose service name over full container name.
			name := c.Service
			if name == "" {
				name = c.Name
			}
			name = Truncate(name, contNameMax)

			stub := tracked && c.State == "running" && !metricsAvail[c.ID]

			// CPU column.
			var cpuStr string
			if !tracked || stub {
				cpuStr = "—"
			} else if c.State != "running" {
				cpuStr = c.State
				if len([]rune(cpuStr)) > cpuW {
					cpuStr = string([]rune(cpuStr)[:cpuW])
				}
			} else {
				cpuStr = fmt.Sprintf("%.1f%%", c.CPUPercent)
			}
			for len([]rune(cpuStr)) < cpuW {
				cpuStr = " " + cpuStr
			}

			// MEM column.
			var memStr string
			if !tracked || stub || c.State != "running" {
				memStr = "—"
			} else {
				memStr = formatBytes(c.MemUsage)
			}
			for len([]rune(memStr)) < memW {
				memStr = " " + memStr
			}

			// Health column.
			var styledHchk string
			if !tracked {
				styledHchk = "  " + muted.Render("—")
			} else {
				styledHchk = "  " + healthIcon(c.Health, theme)
			}

			// Status column (uptime or state).
			var statStr string
			if !tracked || stub {
				statStr = "—"
			} else if c.State != "running" {
				statStr = ""
			} else if c.StartedAt > 0 {
				statStr = formatCompactUptime(now - c.StartedAt)
			}
			for len([]rune(statStr)) < statW {
				statStr = " " + statStr
			}

			// Color the columns.
			var styledCPU, styledMem, styledStat string
			if !tracked || stub || c.State != "running" {
				styledCPU = muted.Render(cpuStr)
				styledMem = muted.Render(memStr)
				styledStat = muted.Render(statStr)
			} else {
				cpuColor := containerCPUColor(c.CPUPercent, c.CPULimit, theme)
				styledCPU = lipgloss.NewStyle().Foreground(cpuColor).Render(cpuStr)
				memColor := containerMemColor(c.MemPercent, c.MemLimit, theme)
				styledMem = lipgloss.NewStyle().Foreground(memColor).Render(memStr)
				styledStat = muted.Render(statStr)
			}

			// Build container row: "  ● name   cpu  mem  ✓  stat"
			prefix := "  " + dot + " " + lipgloss.NewStyle().Foreground(theme.FgBright).Render(name)
			prefixW := lipgloss.Width(prefix)
			gap := w - prefixW - colsW
			if gap < 1 {
				gap = 1
			}

			row := prefix + strings.Repeat(" ", gap) + styledCPU + styledMem + styledHchk + styledStat
			if idx == a.cursor {
				row = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(row), w))
			} else if !tracked {
				row = muted.Render(stripANSI(row))
			}
			lines = append(lines, TruncateStyled(row, w))
		}
	}

	// Scroll viewport follows cursor.
	// Map cursor index to line index for scroll centering.
	cursorLine := 0
	lineIdx := 0
	for idx, item := range items {
		if item.isProject && idx > 0 {
			lineIdx++ // blank line before group
		}
		if idx == a.cursor {
			cursorLine = lineIdx
		}
		lineIdx++
	}

	if len(lines) > maxH {
		start := cursorLine - maxH/2
		if start < 0 {
			start = 0
		}
		if start+maxH > len(lines) {
			start = len(lines) - maxH
		}
		lines = lines[start : start+maxH]
	}

	// Pad to maxH.
	for len(lines) < maxH {
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// cpuHostColor returns severity based on host-relative CPU usage.
// CPU is a shared resource — high usage is always relevant regardless of limits.
func cpuHostColor(cpuPct float64, theme *Theme) lipgloss.Color {
	switch {
	case cpuPct > 25:
		return theme.Critical
	case cpuPct > 8:
		return theme.Warning
	case cpuPct >= 3:
		return theme.Fg
	default:
		return theme.FgDim
	}
}

// containerCPUColor returns a color for CPU percentage.
// Always considers host-relative severity (CPU is zero-sum on the host).
// When a limit exists, also considers limit-relative severity; uses whichever is worse.
func containerCPUColor(cpuPct, cpuLimit float64, theme *Theme) lipgloss.Color {
	hostColor := cpuHostColor(cpuPct, theme)
	if cpuLimit == 0 {
		return hostColor
	}
	pctOfLimit := cpuPct / cpuLimit
	var limitColor lipgloss.Color
	switch {
	case pctOfLimit >= 90:
		limitColor = theme.Critical
	case pctOfLimit >= 70:
		limitColor = theme.Warning
	default:
		limitColor = theme.FgDim
	}
	if colorRank(limitColor, theme) > colorRank(hostColor, theme) {
		return limitColor
	}
	return hostColor
}

// containerMemColor returns a color for memory percentage based on configured limit.
// No limit (memLimit == 0): always dim — no severity without a ceiling.
// Has limit: color by MemPercent (already computed as usage/limit*100).
func containerMemColor(memPct float64, memLimit uint64, theme *Theme) lipgloss.Color {
	if memLimit == 0 {
		return theme.FgDim
	}
	switch {
	case memPct >= 90:
		return theme.Critical
	case memPct >= 70:
		return theme.Warning
	default:
		return theme.FgDim
	}
}

// diskSeverityColor returns a color for disk usage percentage.
// Thresholds align with the alert rule host.disk_percent > 90.
func diskSeverityColor(pct float64, theme *Theme) lipgloss.Color {
	switch {
	case pct >= 90:
		return theme.Critical
	case pct >= 70:
		return theme.Warning
	default:
		return theme.FgDim
	}
}

// loadSeverityColor returns a color for load average based on load1 / CPU count.
func loadSeverityColor(load1 float64, cpus int, theme *Theme) lipgloss.Color {
	if cpus <= 0 {
		cpus = 1
	}
	ratio := load1 / float64(cpus)
	switch {
	case ratio > 1.0:
		return theme.Critical
	case ratio >= 0.7:
		return theme.Warning
	default:
		return theme.FgDim
	}
}

// colorRank returns a severity rank for ordering: FgDim=0, Fg=1, Warning=2, Critical=3.
func colorRank(c lipgloss.Color, theme *Theme) int {
	switch c {
	case theme.Critical:
		return 3
	case theme.Warning:
		return 2
	case theme.Fg:
		return 1
	default:
		return 0
	}
}

// projectStatColor returns the color for a project's running-count column.
// All running + all healthy (or no healthcheck) → Healthy.
// All running + any unhealthy/starting → Warning.
// Some not running → Warning.
// None running → Critical.
func projectStatColor(g containerGroup, theme *Theme) lipgloss.Color {
	if g.running == 0 {
		return theme.Critical
	}
	if g.running < len(g.containers) {
		return theme.Warning
	}
	for _, c := range g.containers {
		if hasHealthcheck(c.Health) && c.Health != "healthy" {
			return theme.Warning
		}
	}
	return theme.Healthy
}

func renderStatusLine(s *Session, w int, theme *Theme) string {
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)
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

func renderHelpBar(a *App, w int, theme *Theme) string {
	dim := lipgloss.NewStyle().Foreground(theme.FgDim)
	bright := lipgloss.NewStyle().Foreground(theme.Fg)

	type binding struct{ key, label string }
	bindings := []binding{
		{"j/k", "navigate"},
		{"enter", "detail"},
		{"space", "expand"},
		{"2", "alerts"},
		{"?", "help"},
		{"q", "quit"},
	}

	var parts []string
	for _, b := range bindings {
		parts = append(parts, bright.Render(b.key)+" "+dim.Render(b.label))
	}

	line := strings.Join(parts, "  ")
	return centerText(line, w)
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
