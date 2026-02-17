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

	// 1. Top bar.
	sections = append(sections, renderDetailTopBar(a, s, contentW))

	// 2. Divider with time range.
	sections = append(sections, renderLabeledDivider(a.windowLabel(), contentW, theme))

	// 3+4. CPU and MEM sparklines (2 rows each).
	sections = append(sections, renderDetailGraphs(det, s, contentW, theme))

	// 5. Limit gauge bars (container only, 0–2 lines).
	var limitLines int
	if !det.isGroupMode() {
		if cm := findContainer(det.containerID, s.Containers); cm != nil {
			if gauge := renderLimitGauges(cm, contentW, theme); gauge != "" {
				sections = append(sections, gauge)
				limitLines = countLines(gauge)
			}
		}
	}

	// 6. Alert banner.
	var alertLines int
	alerts := collectDetailAlerts(det, s.Alerts)
	if len(alerts) > 0 {
		alertStr := renderDetailAlerts(alerts, contentW, theme)
		sections = append(sections, alertStr)
		alertLines = countLines(alertStr)
	}

	// 7. Divider.
	sections = append(sections, renderDivider(contentW, theme))

	// Fixed layout:
	// bird(1) + top bar(1) + time div(2) + graphs(4) + divider(1) + footer(2) = 11
	fixedH := 11 + limitLines + alertLines
	logH := height - fixedH
	if logH < 3 {
		logH = 3
	}

	// 8. Logs.
	sections = append(sections, renderDetailLogs(det, s, contentW, logH, a.display, theme))

	// 9. Footer: help bar.
	sections = append(sections, renderDetailHelp(det, contentW, theme))

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
		modal := renderExpandModal(det.expandModal, width, height, theme, a.tsFormat())
		result = Overlay(result, modal, width, height)
	} else if det.filterModal != nil {
		modal := renderFilterModal(det.filterModal, width, height, theme, a.display)
		result = Overlay(result, modal, width, height)
	} else if det.infoOverlay {
		modal := renderInfoOverlay(det, s, width, height, theme)
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
	escHint := lipgloss.NewStyle().Foreground(theme.Fg).Render("Esc") + " " + muted.Render("←")

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

		// Running count.
		total := len(det.projectIDs)
		running := 0
		for _, id := range det.projectIDs {
			if cm := findContainer(id, s.Containers); cm != nil && cm.State == "running" {
				running++
			}
		}
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
		breadcrumb = escHint + " " + muted.Render(det.svcProject+" /") + " " + lipgloss.NewStyle().Bold(true).Render(containerName)
	} else {
		breadcrumb = escHint + " " + lipgloss.NewStyle().Bold(true).Render(containerName)
	}

	if cm == nil {
		return padBetween(breadcrumb, muted.Render("loading..."), w)
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

	// Health label (only if healthcheck exists).
	if hasHealthcheck(cm.Health) {
		hColor := theme.Healthy
		if cm.Health != "healthy" {
			hColor = theme.Warning
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(hColor).Render(cm.Health))
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

func renderDetailGraphs(det *DetailState, s *Session, w int, theme *Theme) string {
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

	labelW := 4 // "cpu " / "mem "
	pctW := 7
	graphW := w - labelW - pctW
	if graphW < 5 {
		graphW = 5
	}
	indent := strings.Repeat(" ", labelW)
	pctPad := strings.Repeat(" ", pctW)

	// CPU value.
	var cpuVal float64
	if det.isGroupMode() {
		for _, id := range det.projectIDs {
			if cm := findContainer(id, s.Containers); cm != nil {
				cpuVal += cm.CPUPercent
			}
		}
	} else {
		if cm := findContainer(det.containerID, s.Containers); cm != nil {
			cpuVal = cm.CPUPercent
		}
	}
	cpuPct := fmt.Sprintf(" %.1f%%", cpuVal)
	for len(cpuPct) < pctW {
		cpuPct = " " + cpuPct
	}

	// MEM value.
	var memVal uint64
	if det.isGroupMode() {
		for _, id := range det.projectIDs {
			if cm := findContainer(id, s.Containers); cm != nil {
				memVal += cm.MemUsage
			}
		}
	} else {
		if cm := findContainer(det.containerID, s.Containers); cm != nil {
			memVal = cm.MemUsage
		}
	}
	memPct := fmt.Sprintf(" %s", formatBytes(memVal))
	for len(memPct) < pctW {
		memPct = " " + memPct
	}

	cpuTop, cpuBot := Sparkline(det.cpuHist.Data(), graphW, theme.GraphCPU)
	memTop, memBot := Sparkline(det.memHist.Data(), graphW, theme.GraphMem)

	return indent + cpuTop + pctPad + "\n" +
		muted.Render("cpu ") + cpuBot + muted.Render(cpuPct) + "\n" +
		indent + memTop + pctPad + "\n" +
		muted.Render("mem ") + memBot + muted.Render(memPct)
}

func renderLimitGauges(cm *protocol.ContainerMetrics, w int, theme *Theme) string {
	var lines []string

	// CPU limit gauge.
	if cm.CPULimit > 0 {
		pctOfLimit := cm.CPUPercent / cm.CPULimit
		color := theme.FgDim
		switch {
		case pctOfLimit >= 90:
			color = theme.Critical
		case pctOfLimit >= 70:
			color = theme.Warning
		}
		label := fmt.Sprintf("of %.2f CPU", cm.CPULimit)
		lines = append(lines, renderGaugeLine("cpu", pctOfLimit, label, w, color, theme))
	}

	// MEM limit gauge.
	if cm.MemLimit > 0 {
		pct := cm.MemPercent
		color := theme.FgDim
		switch {
		case pct >= 90:
			color = theme.Critical
		case pct >= 70:
			color = theme.Warning
		}
		label := fmt.Sprintf("of %s", formatBytes(cm.MemLimit))
		lines = append(lines, renderGaugeLine("mem", pct, label, w, color, theme))
	}

	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func renderGaugeLine(name string, pct float64, suffix string, w int, color lipgloss.Color, theme *Theme) string {
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)
	style := lipgloss.NewStyle().Foreground(color)

	// Layout: "cpu   ██████████░░░░░░░░░░  48%   of 0.50 CPU"
	prefix := fmt.Sprintf("%-6s", name)
	pctStr := fmt.Sprintf("%3.0f%%", pct)
	// gauge width = total - prefix(6) - pct(6) - suffix - spacing(6)
	suffixW := len(suffix)
	gaugeW := w - 6 - 6 - suffixW - 6
	if gaugeW < 10 {
		gaugeW = 10
	}

	filled := int(pct / 100 * float64(gaugeW))
	if filled > gaugeW {
		filled = gaugeW
	}
	if filled < 0 {
		filled = 0
	}
	empty := gaugeW - filled

	bar := style.Render(strings.Repeat("█", filled)) + muted.Render(strings.Repeat("░", empty))
	return muted.Render(prefix) + bar + "  " + style.Render(pctStr) + "   " + muted.Render(suffix)
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
	innerH := maxH - 1 // reserve 1 line for status
	if innerH < 1 {
		innerH = 1
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

	// Status line (count only data entries, not headers).
	var statusParts []string
	statusParts = append(statusParts, formatNumber(len(data))+" lines")
	if det.filterStream != "" {
		statusParts = append(statusParts, det.filterStream)
	}
	if det.isSearchActive() {
		statusParts = append(statusParts, "SEARCH")
	} else if det.logScroll > 0 {
		statusParts = append(statusParts, "PAUSED")
	} else {
		statusParts = append(statusParts, "LIVE")
	}
	status := muted.Render(strings.Join(statusParts, " · "))
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
		left += " " + strings.Repeat(" ", levelColW)
	}

	overhead := leftW + 1
	msgW := width - overhead
	if msgW < 10 {
		msgW = 10
	}
	msg := Truncate(sanitizeLogMsg(parsed.message), msgW)

	return left + " " + msg
}

// levelColor returns the style for a log level string.
func levelColor(level string, theme *Theme) lipgloss.Style {
	switch level {
	case "WARN":
		return lipgloss.NewStyle().Foreground(theme.Warning)
	case "ERROR":
		return lipgloss.NewStyle().Foreground(theme.Critical)
	default: // INFO, DEBUG
		return lipgloss.NewStyle().Foreground(theme.FgDim)
	}
}

func renderDetailHelp(det *DetailState, w int, theme *Theme) string {
	dim := lipgloss.NewStyle().Foreground(theme.FgDim)
	bright := lipgloss.NewStyle().Foreground(theme.Fg)

	type binding struct{ key, label string }
	bindings := []binding{
		{"Esc", "back"},
		{"j/k", "scroll"},
		{"G", "latest"},
		{"Enter", "expand"},
		{"s", "stream"},
		{"f", "filter"},
	}
	if !det.isGroupMode() {
		bindings = append(bindings, binding{"i", "info"})
	}
	bindings = append(bindings, binding{"+/-", "zoom"})

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

	// Fixed: bird(1) + top bar(1) + time div(2) + graphs(4) + divider(1) + footer(2) = 11
	fixedH := 11

	// Limit gauges.
	if !det.isGroupMode() {
		if cm := findContainer(det.containerID, s.Containers); cm != nil {
			if cm.CPULimit > 0 {
				fixedH++
			}
			if cm.MemLimit > 0 {
				fixedH++
			}
		}
	}

	// Alerts.
	alerts := collectDetailAlerts(det, s.Alerts)
	if len(alerts) > 0 {
		fixedH += len(alerts)
	}

	logH := a.height - fixedH
	if logH < 3 {
		logH = 3
	}
	innerH := logH - 1 // status line
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
	if modalW > width-4 {
		modalW = width - 4
	}

	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

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
	lines = append(lines, fmt.Sprintf(" %-10s %s", "Image", Truncate(image, valW)))

	// State + uptime.
	status := cm.State
	if cm.State == "running" && cm.StartedAt > 0 {
		secs := time.Now().Unix() - cm.StartedAt
		status += " · up " + formatCompactUptime(secs)
	}
	dot := lipgloss.NewStyle().Foreground(theme.StatusDotColor(cm.State, cm.Health)).Render("●")
	lines = append(lines, fmt.Sprintf(" %-10s %s %s", "State", dot, status))

	// Health.
	if hasHealthcheck(cm.Health) {
		hColor := theme.Healthy
		if cm.Health != "healthy" {
			hColor = theme.Warning
		}
		lines = append(lines, fmt.Sprintf(" %-10s %s", "Health", lipgloss.NewStyle().Foreground(hColor).Render(cm.Health)))
	}

	lines = append(lines, fmt.Sprintf(" %-10s %d", "PIDs", cm.PIDs))
	lines = append(lines, fmt.Sprintf(" %-10s %d", "Restarts", cm.RestartCount))
	lines = append(lines, "")

	// Rates.
	rates := s.Rates.ContainerRates[det.containerID]
	rxStyle := lipgloss.NewStyle().Foreground(theme.Healthy)
	txStyle := lipgloss.NewStyle().Foreground(theme.Accent)
	lines = append(lines, fmt.Sprintf(" Net  %s %-12s  %s %s",
		rxStyle.Render("▼"), formatBytesRate(rates.NetRxRate),
		txStyle.Render("▲"), formatBytesRate(rates.NetTxRate)))
	lines = append(lines, fmt.Sprintf(" Blk  %s %-12s  %s %s",
		rxStyle.Render("R"), formatBytesRate(rates.BlockReadRate),
		txStyle.Render("W"), formatBytesRate(rates.BlockWriteRate)))

	// Limits.
	if cm.CPULimit > 0 || cm.MemLimit > 0 {
		lines = append(lines, "")
		if cm.CPULimit > 0 {
			lines = append(lines, fmt.Sprintf(" %-10s %.2f cores", "CPU Limit", cm.CPULimit))
		}
		if cm.MemLimit > 0 {
			lines = append(lines, fmt.Sprintf(" %-10s %s", "Mem Limit", formatBytes(cm.MemLimit)))
		}
	}

	lines = append(lines, "")
	lines = append(lines, " "+muted.Render("i or Esc to close"))

	content := strings.Join(lines, "\n")
	modalH := len(lines) + 2
	if modalH > height-2 {
		modalH = height - 2
	}

	name := serviceNameByID(det.containerID, s.ContInfo)
	if name == "" {
		name = cm.Name
	}
	return renderBox(name, content, modalW, modalH, theme)
}

func renderFilterModal(m *logFilterModal, width, height int, theme *Theme, cfg DisplayConfig) string {
	dateW := len([]rune(cfg.DateFormat))
	timeW := len([]rune(cfg.TimeFormat))

	const prefix = 8
	lineW := prefix + 1 + dateW + 1 + 3 + 1 + timeW + 1
	modalW := lineW + 6
	if modalW < 45 {
		modalW = 45
	}
	if modalW > width-4 {
		modalW = width - 4
	}
	innerW := modalW - 2

	muted := lipgloss.NewStyle().Foreground(theme.FgDim)
	accent := lipgloss.NewStyle().Foreground(theme.Accent)
	cursorStyle := lipgloss.NewStyle().Reverse(true)

	bracket := func(ch string, focused bool) string {
		if focused {
			return accent.Render(ch)
		}
		return muted.Render(ch)
	}

	textFieldLines := func(val string, focused bool) []string {
		maxW := innerW - 4
		if focused {
			maxW--
		}
		if maxW < 4 {
			maxW = 4
		}
		var wrapped []string
		if val == "" {
			wrapped = []string{""}
		} else {
			wrapped = wrapText(val, maxW)
		}
		if focused {
			wrapped[len(wrapped)-1] += cursorStyle.Render(" ")
		}
		return wrapped
	}

	pad := func(s string, w int) string {
		if len(s) >= w {
			return s[:w]
		}
		return s + strings.Repeat(" ", w-len(s))
	}
	hdrDate := pad("date", dateW+5)

	var lines []string
	lines = append(lines, "")
	lines = append(lines, "  Text")
	textLines := textFieldLines(m.text, m.focus == 0)
	for i, tl := range textLines {
		switch {
		case len(textLines) == 1:
			lines = append(lines, "  "+bracket("[", m.focus == 0)+tl+bracket("]", m.focus == 0))
		case i == 0:
			lines = append(lines, "  "+bracket("[", m.focus == 0)+tl)
		case i == len(textLines)-1:
			lines = append(lines, "   "+tl+bracket("]", m.focus == 0))
		default:
			lines = append(lines, "   "+tl)
		}
	}
	lines = append(lines, "")
	lines = append(lines, strings.Repeat(" ", prefix)+muted.Render(hdrDate+"time"))
	lines = append(lines, "  From  "+bracket("[", m.focus == 1)+m.fromDate.render(m.focus == 1, theme)+bracket("]", m.focus == 1)+"   "+bracket("[", m.focus == 2)+m.fromTime.render(m.focus == 2, theme)+bracket("]", m.focus == 2))
	lines = append(lines, "  To    "+bracket("[", m.focus == 3)+m.toDate.render(m.focus == 3, theme)+bracket("]", m.focus == 3)+"   "+bracket("[", m.focus == 4)+m.toTime.render(m.focus == 4, theme)+bracket("]", m.focus == 4))
	lines = append(lines, strings.Repeat(" ", prefix)+muted.Render(pad(cfg.DateFormat, dateW+5)+cfg.TimeFormat))
	lines = append(lines, "")
	lines = append(lines, "  "+muted.Render("Tab next · Enter apply · Esc cancel"))

	content := strings.Join(lines, "\n")
	modalH := len(lines) + 2
	return renderBox("Filter", content, modalW, modalH, theme)
}
