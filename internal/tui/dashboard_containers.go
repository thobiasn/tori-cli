package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

// Column widths for the container list (right-aligned, fixed).
const (
	cpuW   = 6 // " 0.6%" or "13.0%"
	memW   = 8 // "  30.9M" or " 710.1M"
	hchkW  = 3 // "  ✓" or "  ~" or "  ✗"
	statW  = 5 // "  1/1" or "   5d"
	colsW  = cpuW + memW + hchkW + statW
	minGap = 4 // minimum gap between name and columns
)

func renderContainerList(a *App, s *Session, w, maxH int, theme *Theme) string {
	muted := mutedStyle(theme)

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

	now := time.Now().Unix()

	var lines []string
	for idx, item := range items {
		g := a.groups[item.groupIdx]

		if item.isProject {
			if item.groupIdx > 0 && !a.collapsed[a.groups[item.groupIdx-1].name] {
				lines = append(lines, "")
			}
			row := renderProjectRow(a, g, idx, w, s.Alerts, trackedState, metricsAvail, theme)
			lines = append(lines, row)
		} else {
			row := renderContainerRow(g.containers[item.contIdx], idx, a.cursor, w, now, s.Alerts, trackedState, metricsAvail, theme)
			lines = append(lines, row)
		}
	}

	// Scroll viewport follows cursor.
	cursorLine := 0
	lineIdx := 0
	for idx, item := range items {
		if item.isProject && idx > 0 {
			lineIdx++
		}
		if idx == a.cursor {
			cursorLine = lineIdx
		}
		lineIdx++
	}

	return scrollAndPad(lines, cursorLine, maxH)
}

func renderProjectRow(a *App, g containerGroup, idx, w int, alerts map[int64]*protocol.AlertEvent, trackedState, metricsAvail map[string]bool, theme *Theme) string {
	muted := mutedStyle(theme)

	projNameMax := w - 2 - colsW - minGap
	if projNameMax < 8 {
		projNameMax = 8
	}

	chevron := "▾"
	if a.collapsed[g.name] {
		chevron = "▸"
	}

	// Aggregate metrics.
	var cpuSum float64
	var memSum uint64
	allTracked := true
	anyMetrics := false
	worstCPUColor := theme.FgDim
	worstMemColor := theme.FgDim
	collapsed := a.collapsed[g.name]
	for _, c := range g.containers {
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
				continue
			}
			anyMetrics = true
			cpuSum += c.CPUPercent
			memSum += c.MemUsage
			if collapsed {
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
	}

	// Alert indicator: only shown when project is collapsed (children not visible).
	var alertInd string
	if collapsed {
		alertInd = projectAlertIndicator(g, alerts, theme)
	}

	// CPU column.
	var cpuStr string
	if (!allTracked || !anyMetrics) && cpuSum == 0 {
		cpuStr = "—"
	} else {
		cpuStr = fmt.Sprintf("%.1f%%", cpuSum)
	}
	cpuStr = rightAlign(cpuStr, cpuW)

	// MEM column.
	var memStr string
	if (!allTracked || !anyMetrics) && memSum == 0 {
		memStr = "—"
	} else {
		memStr = formatBytes(memSum)
	}
	memStr = rightAlign(memStr, memW)

	styledHchk := "   "

	// Running count column.
	statStr := rightAlign(fmt.Sprintf("%d/%d", g.running, len(g.containers)), statW)

	// Color the columns.
	styledCPU := lipgloss.NewStyle().Foreground(worstCPUColor).Render(cpuStr)
	styledMem := lipgloss.NewStyle().Foreground(worstMemColor).Render(memStr)
	styledStat := lipgloss.NewStyle().Foreground(projectStatColor(g, theme)).Render(statStr)

	// Build project header row.
	chevronStyled := muted.Render(chevron)
	name := Truncate(g.name, projNameMax)
	nameColor := theme.Fg
	if collapsed {
		if severity := projectAlertSeverity(g, alerts); severity == "critical" {
			nameColor = theme.Critical
		} else if severity == "warning" {
			nameColor = theme.Warning
		}
	}
	nameStyled := lipgloss.NewStyle().Foreground(nameColor).Bold(true).Render(name)

	prefix := chevronStyled + " " + nameStyled + alertInd
	prefixW := lipgloss.Width(prefix)
	gap := w - prefixW - colsW
	if gap < 1 {
		gap = 1
	}

	row := prefix + strings.Repeat(" ", gap) + styledCPU + styledMem + styledHchk + styledStat
	if idx == a.cursor {
		row = cursorRow(row, w)
	}
	return TruncateStyled(row, w)
}

func renderContainerRow(c protocol.ContainerMetrics, idx, cursor, w int, now int64, alerts map[int64]*protocol.AlertEvent, trackedState, metricsAvail map[string]bool, theme *Theme) string {
	muted := mutedStyle(theme)
	contNameMax := w - 4 - colsW - minGap
	if contNameMax < 8 {
		contNameMax = 8
	}

	tracked := true
	if t, ok := trackedState[c.ID]; ok {
		tracked = t
	}

	dot := lipgloss.NewStyle().Foreground(theme.StatusDotColor(c.State, c.Health)).Render("●")

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
	cpuStr = rightAlign(cpuStr, cpuW)

	// MEM column.
	var memStr string
	if !tracked || stub || c.State != "running" {
		memStr = "—"
	} else {
		memStr = formatBytes(c.MemUsage)
	}
	memStr = rightAlign(memStr, memW)

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
	statStr = rightAlign(statStr, statW)

	// Color the columns.
	var styledCPU, styledMem, styledStat string
	if !tracked || stub {
		styledCPU = muted.Render(cpuStr)
		styledMem = muted.Render(memStr)
		styledStat = muted.Render(statStr)
	} else if c.State != "running" {
		styledCPU = lipgloss.NewStyle().Foreground(theme.StateColor(c.State)).Render(cpuStr)
		styledMem = muted.Render(memStr)
		styledStat = muted.Render(statStr)
	} else {
		fg := lipgloss.NewStyle().Foreground(theme.Fg)
		cpuColor := containerCPUColor(c.CPUPercent, c.CPULimit, theme)
		if cpuColor == theme.FgDim {
			styledCPU = fg.Render(cpuStr)
		} else {
			styledCPU = lipgloss.NewStyle().Foreground(cpuColor).Render(cpuStr)
		}
		memColor := containerMemColor(c.MemPercent, c.MemLimit, theme)
		if memColor == theme.FgDim {
			styledMem = fg.Render(memStr)
		} else {
			styledMem = lipgloss.NewStyle().Foreground(memColor).Render(memStr)
		}
		styledStat = muted.Render(statStr)
	}

	alertInd := containerAlertIndicator(alerts, c.ID, theme)

	nameColor := theme.FgBright
	if severity := containerAlertSeverity(alerts, c.ID); severity == "critical" {
		nameColor = theme.Critical
	} else if severity == "warning" {
		nameColor = theme.Warning
	} else if hasHealthcheck(c.Health) && c.Health != "healthy" {
		nameColor = theme.Warning
	}
	prefix := "  " + dot + " " + lipgloss.NewStyle().Foreground(nameColor).Render(name) + alertInd
	prefixW := lipgloss.Width(prefix)
	gap := w - prefixW - colsW
	if gap < 1 {
		gap = 1
	}

	row := prefix + strings.Repeat(" ", gap) + styledCPU + styledMem + styledHchk + styledStat
	if idx == cursor {
		row = cursorRow(row, w)
	} else if !tracked {
		row = muted.Render(stripANSI(row))
	}
	return TruncateStyled(row, w)
}

// containerAlertSeverity returns the worst firing alert severity for a container ("critical", "warning", or "").
func containerAlertSeverity(alerts map[int64]*protocol.AlertEvent, containerID string) string {
	suffix := ":" + containerID
	worst := ""
	for _, a := range alerts {
		if a.State != "firing" || !strings.HasSuffix(a.InstanceKey, suffix) {
			continue
		}
		if a.Severity == "critical" {
			return "critical"
		}
		worst = "warning"
	}
	return worst
}

// containerAlertIndicator returns severity-colored ▲ markers for firing alerts on a container.
func containerAlertIndicator(alerts map[int64]*protocol.AlertEvent, containerID string, theme *Theme) string {
	suffix := ":" + containerID
	var warnings, criticals int
	for _, a := range alerts {
		if a.State != "firing" || !strings.HasSuffix(a.InstanceKey, suffix) {
			continue
		}
		if a.Severity == "critical" {
			criticals++
		} else {
			warnings++
		}
	}
	if criticals == 0 && warnings == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(" ")
	crit := lipgloss.NewStyle().Foreground(theme.Critical).Render("▲")
	for range criticals {
		b.WriteString(crit)
	}
	warn := lipgloss.NewStyle().Foreground(theme.Warning).Render("▲")
	for range warnings {
		b.WriteString(warn)
	}
	return b.String()
}

// projectAlertSeverity returns the worst firing alert severity across all containers in a group ("critical", "warning", or "").
func projectAlertSeverity(g containerGroup, alerts map[int64]*protocol.AlertEvent) string {
	worst := ""
	for _, c := range g.containers {
		s := containerAlertSeverity(alerts, c.ID)
		if s == "critical" {
			return "critical"
		}
		if s == "warning" {
			worst = "warning"
		}
	}
	return worst
}

// projectAlertIndicator returns a single ▲ colored by worst alert severity across all containers in a group.
func projectAlertIndicator(g containerGroup, alerts map[int64]*protocol.AlertEvent, theme *Theme) string {
	worst := projectAlertSeverity(g, alerts)
	switch worst {
	case "critical":
		return " " + lipgloss.NewStyle().Foreground(theme.Critical).Render("▲")
	case "warning":
		return " " + lipgloss.NewStyle().Foreground(theme.Warning).Render("▲")
	default:
		return ""
	}
}

