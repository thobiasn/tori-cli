package tui

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
	sections = append(sections, centerText(birdIcon(a.birdBlink, theme), contentW))
	sections = append(sections, "")

	// 2. Header line.
	sections = append(sections, renderAlertsHeader(s, contentW, theme))

	// 3. Divider.
	sections = append(sections, renderSpacedDivider(contentW, theme))

	// 4. Alerts section label.
	sections = append(sections, sectionLabel("alerts", av.focus == sectionAlerts, theme))

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
	sections = append(sections, sectionLabel("rules", av.focus == sectionRules, theme))

	// 8. Rule rows.
	sections = append(sections, renderRuleRows(av, contentW, rulesH, theme))

	// 9. Divider.
	sections = append(sections, renderDivider(contentW, theme))

	// 10. Help bar.
	sections = append(sections, renderAlertsHelp(contentW, theme))

	result := pageFrame(strings.Join(sections, "\n"), contentW, width, height)

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
	muted := mutedStyle(theme)
	sep := styledSep(theme)
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
	muted := mutedStyle(theme)

	items := buildAlertList(s.Alerts, av.resolved, av.showResolved)

	if len(items) == 0 {
		lines := make([]string, maxH)
		if maxH > 1 {
			lines[maxH/2] = centerText(muted.Render("✓ no alerts"), w)
		}
		return strings.Join(lines, "\n")
	}

	var lines []string
	now := time.Now()

	for idx, item := range items {
		row := renderAlertRow(item, w, now, s.ContInfo, theme)

		if av.focus == sectionAlerts && idx == av.alertCursor {
			row = cursorRow(row, w)
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
	muted := mutedStyle(theme)
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
			left += " " + muted.Render("· "+instanceName)
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
	statusLabel := "FIRING"
	if item.acked {
		statusLabel = "ACK"
	}
	right := sev.Render(statusLabel) + " " + sev.Render(dur)
	return padBetween(left, right, w)
}

// renderRuleRows renders the rules section.
func renderRuleRows(av *AlertsState, w, maxH int, theme *Theme) string {
	muted := mutedStyle(theme)

	if len(av.rules) == 0 {
		line := muted.Render("  no rules configured")
		return padLines(line, maxH)
	}

	now := time.Now()

	var lines []string
	for idx, rule := range av.rules {
		row := renderRuleRow(rule, w, now, theme)

		if av.focus == sectionRules && idx == av.ruleCursor {
			row = cursorRow(row, w)
		}
		lines = append(lines, TruncateStyled(row, w))
	}

	return scrollAndPad(lines, av.ruleCursor, maxH)
}

// renderRuleRow renders a single line for a rule with fixed-width columns.
// Layout: "  ▲ WARN  " (10) + name (nameW) + "  " + condition (fill) + action (actionW) + status (statusW)
func renderRuleRow(rule protocol.AlertRuleInfo, w int, now time.Time, theme *Theme) string {
	muted := mutedStyle(theme)
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
		statusText = "silenced"
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

// renderAlertsHelp renders the footer help bar for the alerts view.
func renderAlertsHelp(w int, theme *Theme) string {
	return renderHelpBar([]helpBinding{
		{"tab", "focus"},
		{"j/k", "navigate"},
		{"enter", "details"},
		{"1", "dashboard"},
		{"?", "help"},
		{"q", "quit"},
	}, w, theme)
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

// sectionLabel renders a section name, accented when active, dimmed otherwise.
func sectionLabel(name string, active bool, theme *Theme) string {
	if active {
		return accentStyle(theme).Render(name)
	}
	return mutedStyle(theme).Render(name)
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
