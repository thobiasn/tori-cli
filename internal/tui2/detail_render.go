package tui2

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

// renderDetail renders the full-screen container/group detail view.
func renderDetail(a *App, s *Session, width, height int) string {
	theme := &a.theme

	contentW := width
	if contentW > maxContentW {
		contentW = maxContentW
	}

	det := &s.Detail
	if det.containerID == "" && det.project == "" {
		return ""
	}

	var sections []string

	// 0. Bird.
	bird := "—(•)>"
	if a.birdBlink {
		bird = "—(-)>"
	}
	sections = append(sections, centerText(lipgloss.NewStyle().Foreground(theme.Accent).Render(bird), contentW))
	sections = append(sections, "")

	// 1. Top bar.
	sections = append(sections, renderDetailTopBar(a, s, contentW))

	// 2. Divider with time range.
	sections = append(sections, renderLabeledDivider(a.windowLabel(), contentW, theme))

	// 3+4. CPU and MEM sparklines (2 rows each).
	sections = append(sections, renderDetailGraphs(a, det, s, contentW, theme))

	// 5. Alert banner.
	var alertLines int
	alerts := collectDetailAlerts(det, s.Alerts)
	if len(alerts) > 0 {
		alertStr := renderDetailAlerts(alerts, contentW, theme)
		sections = append(sections, alertStr)
		alertLines = countLines(alertStr)
	}

	// 6. Divider.
	sections = append(sections, renderDivider(contentW, theme))

	// Fixed layout:
	// bird(1) + blank(1) + top bar(1) + time div(2) + graphs(4) + divider(1) + footer(2) = 12
	fixedH := 12 + alertLines
	logH := height - fixedH
	if logH < 3 {
		logH = 3
	}

	// 8. Logs.
	sections = append(sections, renderDetailLogs(det, s, contentW, logH, a.display, theme))

	// 9. Footer: help bar.
	sections = append(sections, renderDetailHelp(contentW, theme))

	content := strings.Join(sections, "\n")

	// Center.
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

	result := strings.Join(lines, "\n")

	// Overlay modals.
	if det.expandModal != nil {
		modal := renderExpandModal(det.expandModal, width, height, theme, a.display)
		result = Overlay(result, modal, width, height)
	} else if det.filterModal != nil {
		modal := renderFilterModal(det.filterModal, width, height, theme, a.display)
		result = Overlay(result, modal, width, height)
	} else if det.infoOverlay {
		var modal string
		if det.isGroupMode() {
			modal = renderProjectInfoDialog(det, s, width, height, theme)
		} else {
			modal = renderInfoOverlay(det, s, width, height, theme)
		}
		result = Overlay(result, modal, width, height)
	}

	return result
}

func renderDetailTopBar(a *App, s *Session, w int) string {
	theme := &a.theme
	det := &s.Detail
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)
	sep := " " + muted.Render("·") + " "

	// Left side: navigation breadcrumb.
	escHint := lipgloss.NewStyle().Foreground(theme.Fg).Render("esc") + " " + muted.Render("←")

	var right string

	if det.isGroupMode() {
		// "Esc ← project                ⚠ 1 alert · 4/4 running"
		left := escHint + " " + lipgloss.NewStyle().Bold(true).Render(det.project)

		// Alert count.
		alerts := collectDetailAlerts(det, s.Alerts)
		var parts []string
		if len(alerts) > 0 {
			label := fmt.Sprintf("⚠ %d alert", len(alerts))
			if len(alerts) > 1 {
				label += "s"
			}
			parts = append(parts, lipgloss.NewStyle().Foreground(theme.Warning).Render(label))
		}

		// Health summary (use ContInfo fallback when metrics haven't arrived).
		hasCheck := false
		worstHealth := "healthy"
		total := len(det.projectIDs)
		running := 0
		for _, id := range det.projectIDs {
			var state, health string
			if cm := findContainer(id, s.Containers); cm != nil {
				state, health = cm.State, cm.Health
			} else if ci := findContInfo(id, s.ContInfo); ci != nil {
				state, health = ci.State, ci.Health
			} else {
				continue
			}
			if state == "running" {
				running++
			}
			if hasHealthcheck(health) {
				hasCheck = true
				if health == "unhealthy" {
					worstHealth = "unhealthy"
				} else if health != "healthy" && worstHealth != "unhealthy" {
					worstHealth = health
				}
			}
		}
		if hasCheck {
			h := worstHealth
			hColor := theme.Healthy
			if h != "healthy" {
				hColor = theme.Critical
				if h == "starting" {
					hColor = theme.Warning
				}
			}
			parts = append(parts, healthIcon(h, theme)+" "+lipgloss.NewStyle().Foreground(hColor).Render(h))
		} else {
			parts = append(parts, lipgloss.NewStyle().Foreground(theme.FgDim).Render("~ no checks"))
		}

		// Running count.
		runColor := theme.Healthy
		if running < total {
			runColor = theme.Warning
		}
		if running == 0 {
			runColor = theme.Critical
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(runColor).Render(fmt.Sprintf("%d/%d running", running, total)))
		right = strings.Join(parts, sep)

		return padBetween(left, right, w)
	}

	// Container view: "Esc ← project / service    ⚠ 1 alert · ● running · healthy · up 13h"
	cm := findContainer(det.containerID, s.Containers)
	containerName := serviceNameByID(det.containerID, s.ContInfo)
	if containerName == "" && cm != nil {
		containerName = cm.Name
	}

	var breadcrumb string
	if det.svcProject != "" {
		proj := Truncate(det.svcProject, 20)
		breadcrumb = escHint + " " + muted.Render(proj+" /") + " " + lipgloss.NewStyle().Bold(true).Render(containerName)
	} else {
		breadcrumb = escHint + " " + lipgloss.NewStyle().Bold(true).Render(containerName)
	}

	if cm == nil {
		dot := lipgloss.NewStyle().Foreground(theme.FgDim).Render("●")
		right = dot + " " + muted.Render("—")
		return padBetween(breadcrumb, right, w)
	}

	var parts []string

	// Alert count.
	alerts := containerAlerts(s.Alerts, det.containerID)
	if len(alerts) > 0 {
		label := fmt.Sprintf("⚠ %d alert", len(alerts))
		if len(alerts) > 1 {
			label += "s"
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(theme.Warning).Render(label))
	}

	// State dot + state.
	dot := lipgloss.NewStyle().Foreground(theme.StatusDotColor(cm.State, cm.Health)).Render("●")
	parts = append(parts, dot+" "+cm.State)

	// Health label.
	if hasHealthcheck(cm.Health) {
		hColor := theme.Healthy
		if cm.Health != "healthy" {
			hColor = theme.Critical
			if cm.Health == "starting" {
				hColor = theme.Warning
			}
		}
		parts = append(parts, healthIcon(cm.Health, theme)+" "+lipgloss.NewStyle().Foreground(hColor).Render(cm.Health))
	} else {
		parts = append(parts, lipgloss.NewStyle().Foreground(theme.FgDim).Render("~ no check"))
	}

	// Uptime.
	if cm.State == "running" && cm.StartedAt > 0 {
		secs := time.Now().Unix() - cm.StartedAt
		parts = append(parts, muted.Render("up "+formatCompactUptime(secs)))
	}

	right = strings.Join(parts, sep)
	return padBetween(breadcrumb, right, w)
}

// padBetween places left and right at the edges of width w.
func padBetween(left, right string, w int) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	gap := w - lw - rw
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func renderDetailGraphs(a *App, det *DetailState, s *Session, w int, theme *Theme) string {
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

	labelW := 4 // "cpu " / "mem "
	pctW := 7
	graphW := w - labelW - pctW
	if graphW < 5 {
		graphW = 5
	}
	indent := strings.Repeat(" ", labelW)
	pctPad := strings.Repeat(" ", pctW)

	// Check if we have any metrics data for this container/group.
	hasMetrics := false
	if det.isGroupMode() {
		for _, id := range det.projectIDs {
			if findContainer(id, s.Containers) != nil {
				hasMetrics = true
				break
			}
		}
	} else {
		hasMetrics = findContainer(det.containerID, s.Containers) != nil
	}

	// Loading state: no metrics data yet — show animated sparklines with dashes.
	if !hasMetrics {
		cpuTop, cpuBot := LoadingSparkline(a.spinnerFrame, graphW, theme.FgDim)
		memTop, memBot := LoadingSparkline(a.spinnerFrame+3, graphW, theme.FgDim)
		dashVal := muted.Render("—")
		rightAlign := func(s string) string {
			w := lipgloss.Width(s)
			if w < pctW {
				return strings.Repeat(" ", pctW-w) + s
			}
			return s
		}
		cpuRight := rightAlign(dashVal)
		memRight := rightAlign(dashVal)
		return indent + cpuTop + pctPad + "\n" +
			muted.Render("cpu ") + cpuBot + cpuRight + "\n" +
			indent + memTop + pctPad + "\n" +
			muted.Render("mem ") + memBot + memRight
	}

	// CPU value.
	var cpuVal float64
	var cpuLimit float64
	if det.isGroupMode() {
		for _, id := range det.projectIDs {
			if cm := findContainer(id, s.Containers); cm != nil {
				cpuVal += cm.CPUPercent
			}
		}
	} else {
		if cm := findContainer(det.containerID, s.Containers); cm != nil {
			cpuVal = cm.CPUPercent
			cpuLimit = cm.CPULimit
		}
	}
	cpuStr := fmt.Sprintf(" %.1f%%", cpuVal)
	for len(cpuStr) < pctW {
		cpuStr = " " + cpuStr
	}

	// MEM value.
	var memVal uint64
	var memLimit uint64
	if det.isGroupMode() {
		for _, id := range det.projectIDs {
			if cm := findContainer(id, s.Containers); cm != nil {
				memVal += cm.MemUsage
			}
		}
	} else {
		if cm := findContainer(det.containerID, s.Containers); cm != nil {
			memVal = cm.MemUsage
			memLimit = cm.MemLimit
		}
	}
	memStr := fmt.Sprintf(" %s", formatBytes(memVal))
	for len(memStr) < pctW {
		memStr = " " + memStr
	}

	cpuTop, cpuBot := Sparkline(det.cpuHist.Data(), graphW, theme.GraphCPU)
	memTop, memBot := Sparkline(det.memHist.Data(), graphW, theme.GraphMem)

	// Severity-colored values with FgBright as calm baseline.
	var memPct float64
	if !det.isGroupMode() {
		if cm := findContainer(det.containerID, s.Containers); cm != nil {
			memPct = cm.MemPercent
		}
	}
	cpuColor := detailCPUColor(cpuVal, cpuLimit, theme)
	memColor := detailMemColor(memPct, memLimit, theme)
	cpuValStyled := lipgloss.NewStyle().Foreground(cpuColor).Render(cpuStr)
	memValStyled := lipgloss.NewStyle().Foreground(memColor).Render(memStr)

	rightAlign := func(s string) string {
		w := lipgloss.Width(s)
		if w < pctW {
			return strings.Repeat(" ", pctW-w) + s
		}
		return s
	}

	// When a limit exists: value on top row (label row), limit on bottom row.
	// When no limit: value on bottom row (same as dashboard).
	var cpuTopRight, cpuBotRight, memTopRight, memBotRight string
	if cpuLimit > 0 {
		cpuTopRight = rightAlign(cpuValStyled)
		cpuBotRight = rightAlign(muted.Render(fmt.Sprintf("/ %.2f", cpuLimit)))
	} else {
		cpuTopRight = pctPad
		cpuBotRight = cpuValStyled
	}
	if memLimit > 0 {
		memTopRight = rightAlign(memValStyled)
		memBotRight = rightAlign(muted.Render("/ " + formatBytes(memLimit)))
	} else {
		memTopRight = pctPad
		memBotRight = memValStyled
	}

	return indent + cpuTop + cpuTopRight + "\n" +
		muted.Render("cpu ") + cpuBot + cpuBotRight + "\n" +
		indent + memTop + memTopRight + "\n" +
		muted.Render("mem ") + memBot + memBotRight
}

// detailCPUColor returns a color for CPU on the detail page.
// Same severity thresholds as the dashboard, but FgBright replaces FgDim/Fg as the calm baseline.
func detailCPUColor(cpuPct, cpuLimit float64, theme *Theme) lipgloss.Color {
	c := containerCPUColor(cpuPct, cpuLimit, theme)
	if c == theme.FgDim || c == theme.Fg {
		return theme.FgBright
	}
	return c
}

// detailMemColor returns a color for memory on the detail page.
// Same severity thresholds as the dashboard, but FgBright replaces FgDim as the calm baseline.
func detailMemColor(memPct float64, memLimit uint64, theme *Theme) lipgloss.Color {
	c := containerMemColor(memPct, memLimit, theme)
	if c == theme.FgDim {
		return theme.FgBright
	}
	return c
}

func collectDetailAlerts(det *DetailState, alerts map[int64]*protocol.AlertEvent) []*protocol.AlertEvent {
	if det.isGroupMode() {
		var out []*protocol.AlertEvent
		for _, id := range det.projectIDs {
			out = append(out, containerAlerts(alerts, id)...)
		}
		return out
	}
	return containerAlerts(alerts, det.containerID)
}

func renderDetailAlerts(alerts []*protocol.AlertEvent, w int, theme *Theme) string {
	var lines []string
	for _, a := range alerts {
		since := ""
		if a.FiredAt > 0 {
			dur := time.Since(time.Unix(a.FiredAt, 0))
			since = formatCompactDuration(dur)
		}
		icon := lipgloss.NewStyle().Foreground(theme.Warning).Render("⚠")
		name := lipgloss.NewStyle().Foreground(theme.FgBright).Render(Truncate(a.RuleName, 20))
		cond := lipgloss.NewStyle().Foreground(theme.FgDim).Render(Truncate(a.Condition, w-40))
		state := lipgloss.NewStyle().Foreground(theme.Warning).Render(a.State)
		line := fmt.Sprintf("%s %s — %s — %s %s", icon, name, cond, state, since)
		lines = append(lines, TruncateStyled(line, w))
	}
	return strings.Join(lines, "\n")
}

func formatCompactDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours())/24)
}

func renderDetailLogs(det *DetailState, s *Session, w, maxH int, cfg DisplayConfig, theme *Theme) string {
	data := det.filteredData()

	// Visible window (accounts for date header lines).
	innerH := maxH - 2 // reserve blank line + status line
	if innerH < 1 {
		innerH = 1
	}

	if len(data) == 0 {
		muted := lipgloss.NewStyle().Foreground(theme.FgDim)
		lines := make([]string, innerH+2)
		if innerH > 1 {
			lines[innerH/2] = centerText(muted.Render("no logs yet"), w)
		}
		return strings.Join(lines, "\n")
	}

	start, end := visibleLogWindow(det, data, innerH, cfg.DateFormat)
	visible := data[start:end]

	// Clamp cursor.
	if det.logCursor >= len(visible) {
		det.logCursor = len(visible) - 1
	}
	if det.logCursor < 0 {
		det.logCursor = 0
	}

	nameW := 0
	if det.isGroupMode() {
		nameW = det.maxSvcNameW
	}

	// Always time-only; date changes are shown via separator headers.
	tsFormat := cfg.TimeFormat
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

	var lines []string
	var prevDate string
	for i, entry := range visible {
		entryDate := time.Unix(entry.Timestamp, 0).Format(cfg.DateFormat)

		// Date separator header on first entry or date change.
		if prevDate == "" || entryDate != prevDate {
			header := centerText(muted.Render("── "+entryDate+" ──"), w)
			lines = append(lines, header)
			prevDate = entryDate
		}

		displayName := ""
		if nameW > 0 {
			displayName = serviceNameByID(entry.ContainerID, s.ContInfo)
			if displayName == "" {
				displayName = entry.ContainerName
			}
		}

		tsStr := time.Unix(entry.Timestamp, 0).Format(tsFormat)
		line := formatLogLine(entry, w, theme, tsStr, nameW, displayName)
		if i == det.logCursor {
			line = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(line), w))
		}
		lines = append(lines, line)
	}

	// Pad or trim to innerH.
	for len(lines) < innerH {
		lines = append(lines, "")
	}
	if len(lines) > innerH {
		lines = lines[len(lines)-innerH:]
	}

	// Blank line + status line.
	lines = append(lines, "")
	sep := muted.Render(" · ")
	countStr := formatNumber(len(data))
	if det.totalLogCount > len(data) {
		countStr += " of " + formatNumber(det.totalLogCount)
	}
	status := muted.Render(countStr + " lines")
	if det.filterStream != "" {
		status += sep + muted.Render(det.filterStream)
	}
	if det.isSearchActive() {
		status += sep + muted.Render("SEARCH")
	} else if det.logScroll > 0 {
		status += sep + muted.Render("PAUSED")
	} else {
		status += sep + lipgloss.NewStyle().Foreground(theme.Healthy).Render("LIVE")
	}
	lines = append(lines, centerText(status, w))

	return strings.Join(lines, "\n")
}

func formatLogLine(entry protocol.LogEntryMsg, width int, theme *Theme, tsStr string, nameW int, displayName string) string {
	tsW := len([]rune(tsStr))
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

	const levelColW = 5
	leftW := tsW
	if nameW > 0 {
		leftW += 1 + nameW
	}
	leftW += 1 + levelColW // space + level column

	// Synthetic lifecycle events.
	if entry.Stream == "event" {
		style := lipgloss.NewStyle().Foreground(theme.FgDim)
		overhead := leftW + 1
		msgW := width - overhead
		if msgW < 10 {
			msgW = 10
		}
		return strings.Repeat(" ", leftW) + " " + style.Render(Truncate(sanitizeLogMsg(entry.Message), msgW))
	}

	// Parse log message for clean text and level.
	parsed := parseLogMessage(entry.Message)
	ts := muted.Render(tsStr)

	var left string
	if nameW > 0 {
		displayed := displayName
		if nameRunes := []rune(displayed); len(nameRunes) > nameW {
			displayed = string(nameRunes[:nameW])
		}
		pad := nameW - len([]rune(displayed))
		left = ts + " " + muted.Render(displayed) + strings.Repeat(" ", pad)
	} else {
		left = ts
	}

	// Level column.
	if parsed.level != "" {
		left += " " + levelColor(parsed.level, theme).Render(fmt.Sprintf("%-5s", parsed.level))
	} else {
		left += " " + muted.Render("─────")
	}

	overhead := leftW + 1
	msgW := width - overhead
	if msgW < 10 {
		msgW = 10
	}
	msgStyle := lipgloss.NewStyle().Foreground(theme.Fg)
	msg := msgStyle.Render(Truncate(sanitizeLogMsg(parsed.message), msgW))

	return left + " " + msg
}

// levelColor returns the style for a log level string.
func levelColor(level string, theme *Theme) lipgloss.Style {
	switch level {
	case "DEBUG":
		return lipgloss.NewStyle().Foreground(theme.DebugLevel)
	case "INFO":
		return lipgloss.NewStyle().Foreground(theme.InfoLevel)
	case "WARN":
		return lipgloss.NewStyle().Foreground(theme.Warning)
	case "ERROR":
		return lipgloss.NewStyle().Foreground(theme.Critical)
	default:
		return lipgloss.NewStyle().Foreground(theme.FgDim)
	}
}

func renderDetailHelp(w int, theme *Theme) string {
	dim := lipgloss.NewStyle().Foreground(theme.FgDim)
	bright := lipgloss.NewStyle().Foreground(theme.Fg)

	type binding struct{ key, label string }
	bindings := []binding{
		{"esc", "back"},
		{"j/k", "scroll"},
		{"f", "filter"},
		{"i", "info"},
		{"?", "help"},
	}

	var parts []string
	for _, b := range bindings {
		parts = append(parts, bright.Render(b.key)+" "+dim.Render(b.label))
	}

	line := strings.Join(parts, "  ")
	return centerText(line, w)
}

// logAreaHeight computes the number of visible log lines.
func logAreaHeight(a *App, det *DetailState, s *Session) int {
	contentW := a.width
	if contentW > maxContentW {
		contentW = maxContentW
	}

	// Fixed: bird(1) + blank(1) + top bar(1) + time div(2) + graphs(4) + divider(1) + footer(2) = 12
	fixedH := 12

	// Alerts.
	alerts := collectDetailAlerts(det, s.Alerts)
	if len(alerts) > 0 {
		fixedH += len(alerts)
	}

	logH := a.height - fixedH
	if logH < 3 {
		logH = 3
	}
	innerH := logH - 2 // blank line + status line
	if innerH < 1 {
		innerH = 1
	}
	return innerH
}

func renderInfoOverlay(det *DetailState, s *Session, width, height int, theme *Theme) string {
	cm := findContainer(det.containerID, s.Containers)
	if cm == nil {
		return ""
	}

	modalW := 56

	// Look up container info.
	var image string
	for _, ci := range s.ContInfo {
		if ci.ID == det.containerID {
			image = ci.Image
			break
		}
	}
	if image == "" {
		image = cm.Image
	}

	valW := modalW - 14

	var lines []string
	lines = append(lines, fmt.Sprintf("%-10s %s", "Image", Truncate(image, valW)))

	// State + uptime.
	status := cm.State
	if cm.State == "running" && cm.StartedAt > 0 {
		secs := time.Now().Unix() - cm.StartedAt
		status += " · up " + formatCompactUptime(secs)
	}
	dot := lipgloss.NewStyle().Foreground(theme.StatusDotColor(cm.State, cm.Health)).Render("●")
	lines = append(lines, fmt.Sprintf("%-10s %s %s", "State", dot, status))

	// Health.
	if hasHealthcheck(cm.Health) {
		hColor := theme.Healthy
		if cm.Health != "healthy" {
			hColor = theme.Critical
			if cm.Health == "starting" {
				hColor = theme.Warning
			}
		}
		lines = append(lines, fmt.Sprintf("%-10s %s %s", "Health", healthIcon(cm.Health, theme), lipgloss.NewStyle().Foreground(hColor).Render(cm.Health)))
	} else {
		lines = append(lines, fmt.Sprintf("%-10s %s", "Health", lipgloss.NewStyle().Foreground(theme.FgDim).Render("~ no check")))
	}

	lines = append(lines, fmt.Sprintf("%-10s %d", "PIDs", cm.PIDs))
	lines = append(lines, fmt.Sprintf("%-10s %d", "Restarts", cm.RestartCount))
	lines = append(lines, "")

	// Rates.
	rates := s.Rates.ContainerRates[det.containerID]
	rxStyle := lipgloss.NewStyle().Foreground(theme.Healthy)
	txStyle := lipgloss.NewStyle().Foreground(theme.Accent)
	lines = append(lines, fmt.Sprintf("Net  %s %-12s  %s %s",
		rxStyle.Render("▼"), formatBytesRate(rates.NetRxRate),
		txStyle.Render("▲"), formatBytesRate(rates.NetTxRate)))
	lines = append(lines, fmt.Sprintf("Blk  %s %-12s  %s %s",
		rxStyle.Render("R"), formatBytesRate(rates.BlockReadRate),
		txStyle.Render("W"), formatBytesRate(rates.BlockWriteRate)))

	// Limits.
	if cm.CPULimit > 0 || cm.MemLimit > 0 {
		lines = append(lines, "")
		if cm.CPULimit > 0 {
			lines = append(lines, fmt.Sprintf("%-10s %.2f cores", "CPU Limit", cm.CPULimit))
		}
		if cm.MemLimit > 0 {
			lines = append(lines, fmt.Sprintf("%-10s %s", "Mem Limit", formatBytes(cm.MemLimit)))
		}
	}

	name := serviceNameByID(det.containerID, s.ContInfo)
	if name == "" {
		name = cm.Name
	}
	return (dialogLayout{
		title: name,
		width: modalW,
		lines: lines,
		tips:  dialogTips(theme, "esc", "close"),
	}).render(width, height, theme)
}

func renderProjectInfoDialog(det *DetailState, s *Session, width, height int, theme *Theme) string {
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

	// Collect container data for this project.
	type row struct {
		name     string
		state    string
		health   string
		cpu      float64
		mem      uint64
		stub     bool  // true = no metrics yet, show "—" for cpu/mem
		uptime   int64 // seconds, 0 if not running
		dotColor lipgloss.Color
	}
	var rows []row
	nameW := 0
	var totalCPU float64
	var totalMem uint64
	anyMetrics := false
	images := make(map[string]struct{})
	running := 0
	allHealthy := true

	now := time.Now().Unix()
	for _, id := range det.projectIDs {
		cm := findContainer(id, s.Containers)
		ci := findContInfo(id, s.ContInfo)

		// Need at least one source of data.
		if cm == nil && ci == nil {
			continue
		}

		name := serviceNameByID(id, s.ContInfo)
		if name == "" && cm != nil {
			name = cm.Name
		}
		if n := len([]rune(name)); n > nameW {
			nameW = n
		}

		var state, health, image string
		var startedAt int64
		var restartCount int
		var cpuVal float64
		var memVal uint64
		isStub := cm == nil

		if cm != nil {
			state, health = cm.State, cm.Health
			startedAt = cm.StartedAt
			restartCount = cm.RestartCount
			cpuVal = cm.CPUPercent
			memVal = cm.MemUsage
			image = cm.Image
		} else {
			state, health = ci.State, ci.Health
			startedAt = ci.StartedAt
			restartCount = ci.RestartCount
			image = ci.Image
		}
		_ = restartCount

		var up int64
		if state == "running" && startedAt > 0 {
			up = now - startedAt
		}
		if state == "running" {
			running++
		}
		if hasHealthcheck(health) && health != "healthy" {
			allHealthy = false
		}

		if !isStub {
			anyMetrics = true
			totalCPU += cpuVal
			totalMem += memVal
		}

		rows = append(rows, row{
			name:     name,
			state:    state,
			health:   health,
			cpu:      cpuVal,
			mem:      memVal,
			stub:     isStub,
			uptime:   up,
			dotColor: theme.StatusDotColor(state, health),
		})
		if image != "" {
			images[image] = struct{}{}
		}
	}
	if nameW > 20 {
		nameW = 20
	}

	// Header line: project name + running count.
	total := len(det.projectIDs)
	var runColor lipgloss.Color
	switch {
	case running == 0:
		runColor = theme.Critical
	case running < total || !allHealthy:
		runColor = theme.Warning
	default:
		runColor = theme.Healthy
	}
	header := lipgloss.NewStyle().Foreground(theme.FgBright).Render(det.project)
	runStr := lipgloss.NewStyle().Foreground(runColor).Render(fmt.Sprintf("%d/%d running", running, total))

	// Compute modal width from content.
	// Row: name + 2 + dot(1) + 1 + state(7) + 2 + health(11) + 2 + cpu(5) + 1 + mem(6) + 2 + uptime(4) = nameW + 44
	rowW := nameW + 44
	headerW := lipgloss.Width(header) + 2 + lipgloss.Width(runStr)
	contentW := rowW
	if headerW > contentW {
		contentW = headerW
	}

	modalW := width * 60 / 100
	if modalW < 50 {
		modalW = 50
	}
	if modalW > 80 {
		modalW = 80
	}
	// Ensure modal fits content + centering padding.
	if minW := contentW + 6; modalW < minW && minW <= 80 {
		modalW = minW
	}

	var lines []string

	// Header.
	gap := contentW - lipgloss.Width(header) - lipgloss.Width(runStr)
	if gap < 2 {
		gap = 2
	}
	lines = append(lines, header+strings.Repeat(" ", gap)+runStr)
	lines = append(lines, "")

	// Container rows.
	for _, r := range rows {
		nameStr := r.name
		if nameRunes := []rune(nameStr); len(nameRunes) > nameW {
			nameStr = string(nameRunes[:nameW])
		}
		namePad := nameW - len([]rune(nameStr))

		dot := lipgloss.NewStyle().Foreground(r.dotColor).Render("●")

		stateStr := fmt.Sprintf("%-7s", r.state)

		var healthStr string
		if hasHealthcheck(r.health) {
			hColor := theme.Healthy
			if r.health != "healthy" {
				hColor = theme.Critical
				if r.health == "starting" {
					hColor = theme.Warning
				}
			}
			healthStr = healthIcon(r.health, theme) + " " + lipgloss.NewStyle().Foreground(hColor).Render(fmt.Sprintf("%-9s", r.health))
		} else {
			healthStr = lipgloss.NewStyle().Foreground(theme.FgDim).Render(fmt.Sprintf("%-11s", "~ no check"))
		}

		var cpuStr, memStr string
		if r.stub {
			cpuStr = muted.Render(fmt.Sprintf("%5s", "—"))
			memStr = muted.Render(fmt.Sprintf("%6s", "—"))
		} else {
			cpuStr = muted.Render(fmt.Sprintf("%5s", fmt.Sprintf("%.1f%%", r.cpu)))
			memStr = muted.Render(fmt.Sprintf("%6s", formatBytes(r.mem)))
		}

		var uptimeStr string
		if r.uptime > 0 {
			uptimeStr = muted.Render(fmt.Sprintf("%4s", formatCompactUptime(r.uptime)))
		} else {
			uptimeStr = strings.Repeat(" ", 4)
		}

		line := nameStr + strings.Repeat(" ", namePad) + "  " +
			dot + " " + stateStr + "  " + healthStr + "  " + cpuStr + " " + memStr + "  " + uptimeStr
		lines = append(lines, line)
	}

	// Summary line.
	lines = append(lines, "")
	var summary string
	if anyMetrics {
		summary = muted.Render(fmt.Sprintf("cpu: %.1f%%  mem: %s  images: %d", totalCPU, formatBytes(totalMem), len(images)))
	} else {
		summary = muted.Render(fmt.Sprintf("cpu: —  mem: —  images: %d", len(images)))
	}
	lines = append(lines, summary)

	return (dialogLayout{
		title: "info",
		width: modalW,
		lines: lines,
		tips:  dialogTips(theme, "esc", "close"),
	}).render(width, height, theme)
}

func renderFilterModal(m *logFilterModal, width, height int, theme *Theme, cfg DisplayConfig) string {
	dateW := len([]rune(cfg.DateFormat))
	timeW := len([]rune(cfg.TimeFormat))

	const labelW = 6 // "From  " / "To    "
	lineW := labelW + 1 + dateW + 1 + 3 + 1 + timeW + 1
	modalW := lineW + 8
	if modalW < 56 {
		modalW = 56
	}

	muted := lipgloss.NewStyle().Foreground(theme.FgDim)
	accent := lipgloss.NewStyle().Foreground(theme.Accent)
	cursorStyle := lipgloss.NewStyle().Reverse(true)

	bracket := func(ch string, focused bool) string {
		if focused {
			return accent.Render(ch)
		}
		return muted.Render(ch)
	}

	pad := func(s string, w int) string {
		if len(s) >= w {
			return s[:w]
		}
		return s + strings.Repeat(" ", w-len(s))
	}

	var lines []string
	lines = append(lines, "")

	// Text label and field.
	lines = append(lines, "Text")
	textMaxW := lineW - 2
	if m.focus == 0 {
		textMaxW--
	}
	if textMaxW < 4 {
		textMaxW = 4
	}
	var textWrapped []string
	if m.text == "" {
		textWrapped = []string{""}
	} else {
		textWrapped = wrapText(m.text, textMaxW)
	}
	if m.focus == 0 {
		textWrapped[len(textWrapped)-1] += cursorStyle.Render(" ")
	}
	for i, tl := range textWrapped {
		switch {
		case len(textWrapped) == 1:
			lines = append(lines, bracket("[", m.focus == 0)+tl+bracket("]", m.focus == 0))
		case i == 0:
			lines = append(lines, bracket("[", m.focus == 0)+tl)
		case i == len(textWrapped)-1:
			lines = append(lines, " "+tl+bracket("]", m.focus == 0))
		default:
			lines = append(lines, " "+tl)
		}
	}

	// Date/time section.
	lines = append(lines, "")
	hdrDate := pad("date", dateW+5)
	lines = append(lines, strings.Repeat(" ", labelW)+muted.Render(hdrDate+"time"))
	lines = append(lines, "From  "+bracket("[", m.focus == 1)+m.fromDate.render(m.focus == 1, theme)+bracket("]", m.focus == 1)+"   "+bracket("[", m.focus == 2)+m.fromTime.render(m.focus == 2, theme)+bracket("]", m.focus == 2))
	lines = append(lines, "To    "+bracket("[", m.focus == 3)+m.toDate.render(m.focus == 3, theme)+bracket("]", m.focus == 3)+"   "+bracket("[", m.focus == 4)+m.toTime.render(m.focus == 4, theme)+bracket("]", m.focus == 4))

	return (dialogLayout{
		title: "Filter",
		width: modalW,
		lines: lines,
		tips:  dialogTips(theme, "tab", "switch", "h/l", "navigate", "enter", "apply", "esc", "cancel"),
	}).render(width, height, theme)
}
