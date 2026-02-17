package tui2

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

// renderAlerts renders the full alerts view.
func renderAlerts(a *App, s *Session, width, height int) string {
	theme := &a.theme

	contentW := width
	if contentW > maxContentW {
		contentW = maxContentW
	}

	av := &s.AlertsView

	var sections []string

	// 1. Bird.
	bird := "—(•)>"
	if a.birdBlink {
		bird = "—(-)>"
	}
	sections = append(sections, centerText(lipgloss.NewStyle().Foreground(theme.Accent).Render(bird), contentW))
	sections = append(sections, "")

	// 2. Header line.
	sections = append(sections, renderAlertsHeader(s, contentW, theme))

	// 3. Divider.
	sections = append(sections, renderSpacedDivider(contentW, theme))

	// 4. Alerts section label.
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)
	alertsLabel := "alerts"
	if av.focus == sectionAlerts {
		alertsLabel = lipgloss.NewStyle().Foreground(theme.Accent).Render("alerts")
	} else {
		alertsLabel = muted.Render("alerts")
	}
	sections = append(sections, alertsLabel)

	// Calculate space: fixed overhead = bird(1) + blank(1) + header(1) + divider(2) + alerts label(1) + divider(1) + rules label(1) + divider(1) + help(1) = 10
	fixedH := 10
	remaining := height - fixedH
	if remaining < 4 {
		remaining = 4
	}
	alertsH := remaining / 2
	rulesH := remaining - alertsH

	// 5. Alert rows.
	sections = append(sections, renderAlertRows(s, contentW, alertsH, theme))

	// 6. Divider.
	sections = append(sections, renderDivider(contentW, theme))

	// 7. Rules section label.
	rulesLabel := "rules"
	if av.focus == sectionRules {
		rulesLabel = lipgloss.NewStyle().Foreground(theme.Accent).Render("rules")
	} else {
		rulesLabel = muted.Render("rules")
	}
	sections = append(sections, rulesLabel)

	// 8. Rule rows.
	sections = append(sections, renderRuleRows(av, contentW, rulesH, theme))

	// 9. Divider.
	sections = append(sections, renderDivider(contentW, theme))

	// 10. Help bar.
	sections = append(sections, renderAlertsHelp(contentW, theme))

	content := strings.Join(sections, "\n")

	// Center the content block.
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

	// Overlay alert/rule detail dialog.
	if av.alertDialog {
		modal := renderAlertDialog(a, s, width, height)
		result = Overlay(result, modal, width, height)
	}
	if av.ruleDialog {
		modal := renderRuleDialog(a, s, width, height)
		result = Overlay(result, modal, width, height)
	}

	// Overlay silence dialog (layers on top of detail dialog).
	if av.silenceModal != nil {
		modal := renderSilenceDialog(av.silenceModal, width, height, theme)
		result = Overlay(result, modal, width, height)
	}

	return result
}

// renderAlertsHeader renders the bird line + server summary.
func renderAlertsHeader(s *Session, w int, theme *Theme) string {
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)
	sep := " " + muted.Render("·") + " "
	nameBold := lipgloss.NewStyle().Bold(true).Render(s.Name)

	firingCount := len(s.Alerts)
	var statusStr string
	if firingCount > 0 {
		statusStr = lipgloss.NewStyle().Foreground(theme.Critical).Render(fmt.Sprintf("%d firing", firingCount))
	} else if s.AlertsView.loaded && len(s.AlertsView.resolved) > 0 {
		statusStr = muted.Render(fmt.Sprintf("%d resolved", len(s.AlertsView.resolved)))
	} else {
		statusStr = lipgloss.NewStyle().Foreground(theme.Healthy).Render("all clear")
	}

	line := nameBold + sep + statusStr
	return centerText(line, w)
}

// renderAlertRows renders the alert list section.
func renderAlertRows(s *Session, w, maxH int, theme *Theme) string {
	av := &s.AlertsView
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

	items := buildAlertList(s.Alerts, av.resolved, av.showResolved)

	if len(items) == 0 {
		line := muted.Render("  no alerts")
		return padLines(line, maxH)
	}

	var lines []string
	now := time.Now()

	for idx, item := range items {
		row := renderAlertRow(item, w, now, s.ContInfo, theme)

		if av.focus == sectionAlerts && idx == av.alertCursor {
			row = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(row), w))
		}
		lines = append(lines, TruncateStyled(row, w))
	}

	// Collapsed resolved summary.
	if !av.showResolved {
		resolvedCount := len(av.resolved)
		if resolvedCount > 0 {
			lines = append(lines, muted.Render(fmt.Sprintf("  %d resolved", resolvedCount)))
		}
	}

	return scrollAndPad(lines, av.alertCursor, maxH)
}

// renderAlertRow renders a single alert row.
// Firing rows are vivid; resolved rows are uniformly dimmed.
func renderAlertRow(item alertItem, w int, now time.Time, contInfo []protocol.ContainerInfo, theme *Theme) string {
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)
	sevColor := severityColor(item.severity, theme)

	// Resolve instance key to human-readable name.
	instanceName := ""
	if item.instanceKey != "" {
		instanceName = instanceDisplayName(item.instanceKey, contInfo)
	}

	if item.resolved {
		// Entire row dimmed.
		icon := muted.Render("▲")
		label := muted.Render(fmt.Sprintf("%-4s", severityLabel(item.severity)))
		name := muted.Render(item.ruleName)

		left := "  " + icon + " " + label + "  " + name
		if instanceName != "" {
			left += " " + muted.Render("· " + instanceName)
		}

		ago := formatCompactDuration(now.Sub(time.Unix(item.resolvedAt, 0)))
		right := muted.Render(ago + " ago")
		return padBetween(left, right, w)
	}

	// Firing — vivid.
	icon := lipgloss.NewStyle().Foreground(sevColor).Render("▲")
	label := lipgloss.NewStyle().Foreground(sevColor).Render(fmt.Sprintf("%-4s", severityLabel(item.severity)))
	name := lipgloss.NewStyle().Foreground(theme.FgBright).Render(item.ruleName)

	left := "  " + icon + " " + label + "  " + name
	if instanceName != "" {
		left += " " + muted.Render("·") + " " + lipgloss.NewStyle().Foreground(theme.Fg).Render(instanceName)
	}

	dur := formatCompactDuration(now.Sub(time.Unix(item.firedAt, 0)))
	sev := lipgloss.NewStyle().Foreground(sevColor)
	right := sev.Render("FIRING") + " " + sev.Render(dur)
	return padBetween(left, right, w)
}

// renderRuleRows renders the rules section.
func renderRuleRows(av *AlertsState, w, maxH int, theme *Theme) string {
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

	if len(av.rules) == 0 {
		line := muted.Render("  no rules configured")
		return padLines(line, maxH)
	}

	now := time.Now()

	var lines []string
	for idx, rule := range av.rules {
		row := renderRuleRow(rule, w, now, theme)

		if av.focus == sectionRules && idx == av.ruleCursor {
			row = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(row), w))
		}
		lines = append(lines, TruncateStyled(row, w))
	}

	return scrollAndPad(lines, av.ruleCursor, maxH)
}

// renderRuleRow renders a single line for a rule with fixed-width columns.
// Layout: "  ▲ WARN  " (10) + name (nameW) + "  " + condition (fill) + action (actionW) + status (statusW)
func renderRuleRow(rule protocol.AlertRuleInfo, w int, now time.Time, theme *Theme) string {
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)
	sevColor := severityColor(rule.Severity, theme)

	const prefixW = 10 // "  ▲ WARN  "
	const nameW = 18
	const actionW = 8
	const statusW = 12

	icon := lipgloss.NewStyle().Foreground(sevColor).Render("▲")
	label := lipgloss.NewStyle().Foreground(sevColor).Render(fmt.Sprintf("%-4s", severityLabel(rule.Severity)))

	// Name — fixed width, truncated and padded.
	nameStr := Truncate(rule.Name, nameW)
	for len(nameStr) < nameW {
		nameStr += " "
	}
	nameStyled := lipgloss.NewStyle().Foreground(theme.Fg).Bold(true).Render(nameStr)

	// Condition — fills remaining space.
	condW := w - prefixW - nameW - 2 - actionW - statusW
	if condW < 4 {
		condW = 4
	}
	condStr := Truncate(rule.Condition, condW)
	for len(condStr) < condW {
		condStr += " "
	}
	condStyled := muted.Render(condStr)

	// Action — right-aligned within actionW.
	actionText := ""
	if len(rule.Actions) > 0 {
		actionText = "notify"
	}
	actionPad := actionW - len(actionText)
	if actionPad < 0 {
		actionPad = 0
	}
	actionStyled := strings.Repeat(" ", actionPad) + muted.Render(actionText)

	// Status — right-aligned within statusW.
	var statusText string
	var statusStyle lipgloss.Style
	if rule.SilencedUntil > 0 && time.Unix(rule.SilencedUntil, 0).After(now) {
		remaining := formatCompactDuration(time.Until(time.Unix(rule.SilencedUntil, 0)))
		statusText = "silenced " + remaining
		statusStyle = muted
	} else if rule.FiringCount > 0 {
		if rule.FiringCount == 1 {
			statusText = "firing"
		} else {
			statusText = fmt.Sprintf("firing(%d)", rule.FiringCount)
		}
		statusStyle = lipgloss.NewStyle().Foreground(sevColor)
	} else {
		statusText = "ok"
		statusStyle = lipgloss.NewStyle().Foreground(theme.Healthy)
	}
	statusPad := statusW - len(statusText)
	if statusPad < 0 {
		statusPad = 0
	}
	statusStyled := strings.Repeat(" ", statusPad) + statusStyle.Render(statusText)

	return "  " + icon + " " + label + "  " + nameStyled + "  " + condStyled + actionStyled + statusStyled
}

// renderAlertDialog renders a centered overlay with alert details.
func renderAlertDialog(a *App, s *Session, width, height int) string {
	theme := &a.theme
	av := &s.AlertsView
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)
	fg := lipgloss.NewStyle().Foreground(theme.Fg)

	items := buildAlertList(s.Alerts, av.resolved, av.showResolved)
	if av.alertCursor < 0 || av.alertCursor >= len(items) {
		return ""
	}
	item := items[av.alertCursor]

	modalW := width * 60 / 100
	if modalW < 50 {
		modalW = 50
	}
	if modalW > 80 {
		modalW = 80
	}
	innerW := modalW - 2

	sevColor := severityColor(item.severity, theme)
	const labelW = 12

	var lines []string

	header := lipgloss.NewStyle().Foreground(sevColor).Render("▲") + " " +
		lipgloss.NewStyle().Foreground(sevColor).Render(fmt.Sprintf("%-4s", severityLabel(item.severity))) + "  " +
		lipgloss.NewStyle().Bold(true).Foreground(theme.FgBright).Render(item.ruleName)
	lines = append(lines, header)
	lines = append(lines, "")

	if item.instanceKey != "" {
		lines = append(lines, muted.Render("instance:   ")+fg.Render(instanceDisplayName(item.instanceKey, s.ContInfo)))
	}
	lines = append(lines, muted.Render("condition:  ")+fg.Render(item.condition))

	if item.message != "" {
		valueW := innerW - 4 - labelW
		if valueW < 10 {
			valueW = 10
		}
		wrapped := wrapText(item.message, valueW)
		for i, wl := range wrapped {
			if i == 0 {
				lines = append(lines, muted.Render("message:    ")+fg.Render(wl))
			} else {
				lines = append(lines, strings.Repeat(" ", labelW)+fg.Render(wl))
			}
		}
	}

	firedStr := time.Unix(item.firedAt, 0).Format(a.tsFormat())
	lines = append(lines, muted.Render("fired:      ")+fg.Render(firedStr))

	if item.resolved {
		resolvedStr := time.Unix(item.resolvedAt, 0).Format(a.tsFormat())
		lines = append(lines, muted.Render("resolved:   ")+fg.Render(resolvedStr))
	}

	ackStr := "no"
	if item.acked {
		ackStr = "yes"
	}
	lines = append(lines, muted.Render("acked:      ")+fg.Render(ackStr))

	// Build tips.
	var tipBindings []string
	if !item.resolved {
		tipBindings = append(tipBindings, "a", "ack")
	}
	tipBindings = append(tipBindings, "s", "silence")
	if instanceKeyContainerID(item.instanceKey) != "" {
		tipBindings = append(tipBindings, "g", "container")
	}
	tipBindings = append(tipBindings, "j/k", "navigate", "esc", "close")

	return (dialogLayout{
		title: "alert",
		width: modalW,
		lines: lines,
		tips:  dialogTips(theme, tipBindings...),
	}).render(width, height, theme)
}

// renderRuleDialog renders a centered overlay with rule details.
func renderRuleDialog(a *App, s *Session, width, height int) string {
	theme := &a.theme
	av := &s.AlertsView
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)
	fg := lipgloss.NewStyle().Foreground(theme.Fg)

	if av.ruleCursor < 0 || av.ruleCursor >= len(av.rules) {
		return ""
	}
	rule := av.rules[av.ruleCursor]

	modalW := width * 60 / 100
	if modalW < 50 {
		modalW = 50
	}
	if modalW > 80 {
		modalW = 80
	}

	sevColor := severityColor(rule.Severity, theme)
	now := time.Now()

	var lines []string

	header := lipgloss.NewStyle().Foreground(sevColor).Render("▲") + " " +
		lipgloss.NewStyle().Foreground(sevColor).Render(fmt.Sprintf("%-4s", severityLabel(rule.Severity))) + "  " +
		lipgloss.NewStyle().Bold(true).Foreground(theme.FgBright).Render(rule.Name)
	lines = append(lines, header)
	lines = append(lines, "")

	lines = append(lines, muted.Render("condition:  ")+fg.Render(rule.Condition))

	if rule.For != "" && rule.For != "0s" {
		lines = append(lines, muted.Render("for:        ")+fg.Render(rule.For))
	}

	actionsStr := "none"
	if len(rule.Actions) > 0 {
		actionsStr = strings.Join(rule.Actions, ", ")
	}
	lines = append(lines, muted.Render("actions:    ")+fg.Render(actionsStr))

	silencedStr := "no"
	if rule.SilencedUntil > 0 && time.Unix(rule.SilencedUntil, 0).After(now) {
		remaining := formatCompactDuration(time.Until(time.Unix(rule.SilencedUntil, 0)))
		silencedStr = remaining + " remaining"
	}
	lines = append(lines, muted.Render("silenced:   ")+fg.Render(silencedStr))

	if rule.FiringCount > 0 {
		firingStr := fmt.Sprintf("%d instances", rule.FiringCount)
		if rule.FiringCount == 1 {
			firingStr = "1 instance"
		}
		lines = append(lines, muted.Render("firing:     ")+lipgloss.NewStyle().Foreground(sevColor).Render(firingStr))
	}

	return (dialogLayout{
		title: "rule",
		width: modalW,
		lines: lines,
		tips:  dialogTips(theme, "s", "silence", "j/k", "navigate", "esc", "close"),
	}).render(width, height, theme)
}

// renderSilenceDialog renders the silence duration picker modal.
func renderSilenceDialog(m *silenceModalState, width, height int, theme *Theme) string {
	accent := lipgloss.NewStyle().Foreground(theme.Accent)
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

	var parts []string
	for i, d := range silenceDurations {
		if i == m.cursor {
			parts = append(parts, accent.Bold(true).Render(d.label))
		} else {
			parts = append(parts, muted.Render(d.label))
		}
	}

	lines := []string{"", strings.Join(parts, "   ")}

	return (dialogLayout{
		title: "Silence",
		width: 43,
		lines: lines,
		tips:  dialogTips(theme, "h/l", "navigate", "enter", "apply", "esc", "cancel"),
	}).render(width, height, theme)
}

// renderAlertsHelp renders the footer help bar for the alerts view.
func renderAlertsHelp(w int, theme *Theme) string {
	dim := lipgloss.NewStyle().Foreground(theme.FgDim)
	bright := lipgloss.NewStyle().Foreground(theme.Fg)

	type binding struct{ key, label string }
	bindings := []binding{
		{"tab", "focus"},
		{"j/k", "navigate"},
		{"enter", "details"},
		{"1", "dashboard"},
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


// severityColor returns the theme color for an alert severity.
func severityColor(severity string, theme *Theme) lipgloss.Color {
	switch strings.ToLower(severity) {
	case "critical":
		return theme.Critical
	default:
		return theme.Warning
	}
}

// severityLabel returns a short label for severity.
func severityLabel(severity string) string {
	switch strings.ToLower(severity) {
	case "critical":
		return "CRIT"
	default:
		return "WARN"
	}
}

// scrollAndPad scrolls a list to center the cursor, then pads to maxH.
func scrollAndPad(lines []string, cursorLine, maxH int) string {
	if len(lines) > maxH {
		start := cursorLine - maxH/2
		if start < 0 {
			start = 0
		}
		if start+maxH > len(lines) {
			start = len(lines) - maxH
		}
		if start < 0 {
			start = 0
		}
		lines = lines[start : start+maxH]
	}
	for len(lines) < maxH {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// padLines pads a single line to maxH.
func padLines(line string, maxH int) string {
	lines := []string{line}
	for len(lines) < maxH {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
