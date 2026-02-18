package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// renderExpandModal renders a centered overlay showing the full log message.
func renderExpandModal(m *logExpandModal, width, height int, theme *Theme, cfg DisplayConfig) string {
	modalW := width * 3 / 4
	if modalW < 40 {
		modalW = 40
	}
	if modalW > width-4 {
		modalW = width - 4
	}
	modalH := height * 2 / 3
	if modalH < 10 {
		modalH = 10
	}
	innerW := modalW - 2
	innerH := modalH - 2
	if innerH < 1 {
		innerH = 1
	}

	const pad = 2
	contentW := innerW - pad*2
	if contentW < 10 {
		contentW = 10
	}
	padStr := strings.Repeat(" ", pad)

	muted := mutedStyle(theme)
	fg := lipgloss.NewStyle().Foreground(theme.FgBright)

	// Metadata line: time  [container]  LEVEL  stream · full datetime
	t := time.Unix(m.entry.Timestamp, 0)
	timeStr := muted.Render(t.Format(cfg.TimeFormat))
	fullDT := muted.Render(t.Format(cfg.DateFormat + " " + cfg.TimeFormat))

	parsed := parseLogMessage(m.entry.Message)
	var levelStr string
	if parsed.level != "" {
		levelStr = levelColor(parsed.level, theme).Render(parsed.level)
	}

	parts := []string{timeStr}
	if m.project != "" {
		parts = append(parts, fg.Render(m.serviceName))
	}
	if levelStr != "" {
		parts = append(parts, levelStr)
	}
	parts = append(parts, muted.Render(m.entry.Stream)+" "+muted.Render("·")+" "+fullDT)
	metaLine := padStr + strings.Join(parts, "  ")

	// Header: blank + meta + blank.
	// Footer: blank + blank + tips.
	// Fixed lines = 5 (top blank, meta, blank after meta, 2x blank before tips, tips).
	fixedLines := 6

	bodyH := innerH - fixedLines
	if bodyH < 1 {
		bodyH = 1
	}

	// Message body.
	msg := sanitizeLogMsg(m.entry.Message)
	if json.Valid([]byte(msg)) {
		var buf bytes.Buffer
		if json.Indent(&buf, []byte(msg), "", "  ") == nil {
			msg = buf.String()
		}
	}

	wrapped := wrapText(msg, contentW)
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}

	maxScroll := len(wrapped) - bodyH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}

	start := m.scroll
	end := start + bodyH
	if end > len(wrapped) {
		end = len(wrapped)
	}

	var lines []string
	lines = append(lines, "")
	lines = append(lines, metaLine)
	lines = append(lines, "")
	for _, l := range wrapped[start:end] {
		lines = append(lines, padStr+fg.Render(l))
	}

	// Pad to fill available space.
	used := len(lines) + 3 // 2 blank + tips
	for i := used; i < innerH; i++ {
		lines = append(lines, "")
	}

	// Footer tips (centered).
	tipLine := dialogTips(theme, "j/k", "next/prev", "esc", "close")
	tipPad := (innerW - lipgloss.Width(tipLine)) / 2
	if tipPad < 1 {
		tipPad = 1
	}
	lines = append(lines, "")
	lines = append(lines, "")
	lines = append(lines, strings.Repeat(" ", tipPad)+tipLine)

	content := strings.Join(lines, "\n")
	return renderBox("log", content, modalW, modalH, theme)
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
	styledStatus := lipgloss.NewStyle().Foreground(theme.StatusDotColor(cm.State, cm.Health)).Render(status)
	lines = append(lines, fmt.Sprintf("%-10s %s %s", "State", dot, styledStatus))

	// Health.
	lines = append(lines, fmt.Sprintf("%-10s %s", "Health", healthLabel(cm.Health, false, theme)))

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
	muted := mutedStyle(theme)

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

		stateStr := lipgloss.NewStyle().Foreground(r.dotColor).Render(fmt.Sprintf("%-7s", r.state))

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
			fg := lipgloss.NewStyle().Foreground(theme.Fg)
			cpuStr = fg.Render(fmt.Sprintf("%5s", fmt.Sprintf("%.1f%%", r.cpu)))
			memStr = fg.Render(fmt.Sprintf("%6s", formatBytes(r.mem)))
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
	bright := lipgloss.NewStyle().Foreground(theme.FgBright)
	if anyMetrics {
		summary = muted.Render("cpu: ") + bright.Render(fmt.Sprintf("%.1f%%", totalCPU)) +
			muted.Render("  mem: ") + bright.Render(formatBytes(totalMem)) +
			muted.Render(fmt.Sprintf("  images: %d", len(images)))
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

	muted := mutedStyle(theme)
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
