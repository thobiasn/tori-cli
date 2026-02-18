package tui

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
	sections = append(sections, centerText(birdIcon(a.birdBlink, theme), contentW))
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
		sections = append(sections, "") // blank line before alerts
		alertStr := renderDetailAlerts(alerts, contentW, theme)
		sections = append(sections, alertStr)
		alertLines = 1 + countLines(alertStr)
	}

	// 6. Divider.
	sections = append(sections, renderDivider(contentW, theme))

	// Fixed layout:
	// bird(1) + blank(1) + top bar(1) + time div(2) + graphs(4) + divider(1) + divider(1) + status(1) + help(1) = 13
	fixedH := 13 + alertLines
	if det.isSearchActive() {
		fixedH += 2 // filter divider(1) + filter line(1)
	}
	logH := height - fixedH
	if logH < 3 {
		logH = 3
	}

	// 8. Logs.
	sections = append(sections, renderDetailLogs(det, s, contentW, logH, a.display, theme))

	// 8.5. Filter bar (when search/filter is active).
	if det.isSearchActive() {
		sections = append(sections, renderDivider(contentW, theme))
		sections = append(sections, renderFilterBar(det, contentW, a.display, theme))
	}

	// 9. Divider.
	sections = append(sections, renderDivider(contentW, theme))

	// 10. Log status line.
	sections = append(sections, renderLogStatus(det, contentW, theme))

	// 11. Footer: help bar.
	sections = append(sections, renderDetailHelp(contentW, det.isSearchActive(), theme))

	result := pageFrame(strings.Join(sections, "\n"), contentW, width, height)

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
	muted := mutedStyle(theme)
	sep := styledSep(theme)

	// Left side: navigation breadcrumb.
	escHint := lipgloss.NewStyle().Foreground(theme.Fg).Render("esc") + " " + muted.Render("←")

	var right string

	if det.isGroupMode() {
		// "Esc ← project                ▲ 1 alert · 4/4 running"
		left := escHint + " " + lipgloss.NewStyle().Bold(true).Render(det.project)

		// Alert count.
		alerts := collectDetailAlerts(det, s.Alerts)
		var parts []string
		if len(alerts) > 0 {
			label := fmt.Sprintf("▲ %d alert", len(alerts))
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
			parts = append(parts, healthLabel(worstHealth, true, theme))
		} else {
			parts = append(parts, healthLabel("", true, theme))
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

	// Container view: "Esc ← project / service    ▲ 1 alert · ● running · healthy · up 13h"
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
		label := fmt.Sprintf("▲ %d alert", len(alerts))
		if len(alerts) > 1 {
			label += "s"
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(theme.Warning).Render(label))
	}

	// State dot + state.
	dot := lipgloss.NewStyle().Foreground(theme.StatusDotColor(cm.State, cm.Health)).Render("●")
	stateStyled := lipgloss.NewStyle().Foreground(theme.StatusDotColor(cm.State, cm.Health)).Render(cm.State)
	parts = append(parts, dot+" "+stateStyled)

	// Health label.
	parts = append(parts, healthLabel(cm.Health, false, theme))

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
	muted := mutedStyle(theme)

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
	anyRunning := false
	if det.isGroupMode() {
		for _, id := range det.projectIDs {
			if cm := findContainer(id, s.Containers); cm != nil {
				hasMetrics = true
				if cm.State == "running" {
					anyRunning = true
					break
				}
			}
		}
	} else {
		if cm := findContainer(det.containerID, s.Containers); cm != nil {
			hasMetrics = true
			anyRunning = cm.State == "running"
		}
	}

	// Loading state: no metrics, no running containers, or backfill in-flight.
	if !hasMetrics || !anyRunning || det.metricsBackfillPending {
		cpuTop, cpuBot := LoadingSparkline(a.spinnerFrame, graphW, theme.FgDim)
		memTop, memBot := LoadingSparkline(a.spinnerFrame+3, graphW, theme.FgDim)
		cpuRight := pctPad
		memRight := pctPad
		if hasMetrics {
			// Show current live values while graphs are loading.
			var cpuVal float64
			var memVal uint64
			if det.isGroupMode() {
				for _, id := range det.projectIDs {
					if cm := findContainer(id, s.Containers); cm != nil {
						cpuVal += cm.CPUPercent
						memVal += cm.MemUsage
					}
				}
			} else {
				if cm := findContainer(det.containerID, s.Containers); cm != nil {
					cpuVal = cm.CPUPercent
					memVal = cm.MemUsage
				}
			}
			cpuRight = muted.Render(rightAlign(fmt.Sprintf(" %.1f%%", cpuVal), pctW))
			memRight = muted.Render(rightAlign(fmt.Sprintf(" %s", formatBytes(memVal)), pctW))
		} else {
			dashVal := muted.Render("—")
			ra := func(s string) string {
				w := lipgloss.Width(s)
				if w < pctW {
					return strings.Repeat(" ", pctW-w) + s
				}
				return s
			}
			cpuRight = ra(dashVal)
			memRight = ra(dashVal)
		}
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
	cpuStr := rightAlign(fmt.Sprintf(" %.1f%%", cpuVal), pctW)

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
	memStr := rightAlign(fmt.Sprintf(" %s", formatBytes(memVal)), pctW)

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
		sevColor := theme.Warning
		if a.Severity == "critical" {
			sevColor = theme.Critical
		}
		icon := lipgloss.NewStyle().Foreground(sevColor).Render("▲")
		name := lipgloss.NewStyle().Foreground(sevColor).Render(Truncate(a.RuleName, 20))
		cond := lipgloss.NewStyle().Foreground(theme.FgDim).Render(Truncate(a.Condition, w-40))
		state := lipgloss.NewStyle().Foreground(sevColor).Render(a.State)
		line := fmt.Sprintf("%s %s — %s — %s %s", icon, name, cond, state, since)
		lines = append(lines, centerText(TruncateStyled(line, w), w))
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

	// Visible window.
	innerH := maxH
	if innerH < 1 {
		innerH = 1
	}

	if len(data) == 0 {
		muted := mutedStyle(theme)
		lines := make([]string, innerH)
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
	muted := mutedStyle(theme)

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
			line = cursorRow(line, w)
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

	return strings.Join(lines, "\n")
}

func renderLogStatus(det *DetailState, w int, theme *Theme) string {
	muted := mutedStyle(theme)
	sep := muted.Render(" · ")

	fg := lipgloss.NewStyle().Foreground(theme.Fg)
	data := det.filteredData()
	countStr := fg.Render(formatNumber(len(data)))
	if det.totalLogCount > len(data) {
		countStr += muted.Render(" of ") + fg.Render(formatNumber(det.totalLogCount))
	}
	status := countStr + muted.Render(" lines")
	if det.filterStream != "" {
		status += sep + muted.Render(det.filterStream)
	}
	if det.isSearchActive() || det.logPaused {
		status += sep + muted.Render("PAUSED")
	} else {
		status += sep + lipgloss.NewStyle().Foreground(theme.Healthy).Render("LIVE")
	}
	return centerText(status, w)
}

func formatLogLine(entry protocol.LogEntryMsg, width int, theme *Theme, tsStr string, nameW int, displayName string) string {
	tsW := len([]rune(tsStr))
	muted := mutedStyle(theme)

	// Parse log message for clean text and level.
	parsed := parseLogMessage(entry.Message)

	// Synthetic lifecycle events — centered like date separators.
	if entry.Stream == "event" {
		style := lipgloss.NewStyle().Foreground(theme.FgDim)
		return centerText(style.Render("— "+sanitizeLogMsg(entry.Message)+" —"), width)
	}

	// Left side: "ts [name] [level]" with natural single-space separation.
	left := muted.Render(tsStr)
	leftUsed := tsW
	if nameW > 0 {
		displayed := displayName
		if nameRunes := []rune(displayed); len(nameRunes) > nameW {
			displayed = string(nameRunes[:nameW])
		}
		left += " " + muted.Render(displayed)
		leftUsed += 1 + len([]rune(displayed))
	}
	if parsed.level != "" {
		left += " " + levelColor(parsed.level, theme).Render(parsed.level)
		leftUsed += 1 + len(parsed.level)
	}

	overhead := leftUsed + 1
	msgW := width - overhead
	if msgW < 10 {
		msgW = 10
	}
	msgStyle := lipgloss.NewStyle().Foreground(theme.FgBright)
	msg := msgStyle.Render(Truncate(sanitizeLogMsg(parsed.message), msgW))

	return left + " " + msg
}

// levelColor returns the style for a log level string.
func levelColor(level string, theme *Theme) lipgloss.Style {
	switch level {
	case "DBUG":
		return lipgloss.NewStyle().Foreground(theme.DebugLevel)
	case "INFO":
		return lipgloss.NewStyle().Foreground(theme.InfoLevel)
	case "WARN":
		return lipgloss.NewStyle().Foreground(theme.Warning)
	case "ERR":
		return lipgloss.NewStyle().Foreground(theme.Critical)
	default:
		return lipgloss.NewStyle().Foreground(theme.FgDim)
	}
}

func renderDetailHelp(w int, searchActive bool, theme *Theme) string {
	escLabel := "back"
	if searchActive {
		escLabel = "clear filter"
	}
	return renderHelpBar([]helpBinding{
		{"esc", escLabel},
		{"j/k", "scroll"},
		{"f", "filter"},
		{"i", "info"},
		{"?", "help"},
	}, w, theme)
}

func renderFilterBar(det *DetailState, w int, cfg DisplayConfig, theme *Theme) string {
	muted := mutedStyle(theme)
	fg := lipgloss.NewStyle().Foreground(theme.Fg)
	sep := muted.Render(" · ")

	var parts []string
	if det.searchText != "" {
		parts = append(parts, muted.Render("search ")+fg.Render(Truncate(det.searchText, 20)))
	}
	if det.filterFrom != 0 {
		t := time.Unix(det.filterFrom, 0)
		parts = append(parts, muted.Render("from ")+fg.Render(t.Format(cfg.DateFormat+" "+cfg.TimeFormat)))
	}
	if det.filterTo != 0 {
		t := time.Unix(det.filterTo, 0)
		parts = append(parts, muted.Render("to ")+fg.Render(t.Format(cfg.DateFormat+" "+cfg.TimeFormat)))
	}

	return centerText(strings.Join(parts, sep), w)
}

// logAreaHeight computes the number of visible log lines.
func logAreaHeight(a *App, det *DetailState, s *Session) int {
	contentW := a.width
	if contentW > maxContentW {
		contentW = maxContentW
	}

	// Fixed: bird(1) + blank(1) + top bar(1) + time div(2) + graphs(4) + divider(1) + divider(1) + status(1) + help(1) = 13
	fixedH := 13

	// Alerts.
	alerts := collectDetailAlerts(det, s.Alerts)
	if len(alerts) > 0 {
		fixedH += len(alerts)
	}

	// Filter bar.
	if det.isSearchActive() {
		fixedH += 2 // filter divider(1) + filter line(1)
	}

	logH := a.height - fixedH
	if logH < 1 {
		logH = 1
	}
	return logH
}
