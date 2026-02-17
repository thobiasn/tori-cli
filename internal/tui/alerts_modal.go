package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// renderAlertDialog renders a centered overlay with alert details.
func renderAlertDialog(a *App, s *Session, width, height int) string {
	theme := &a.theme
	av := &s.AlertsView
	muted := mutedStyle(theme)
	fg := fgStyle(theme)

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
	muted := mutedStyle(theme)
	fg := fgStyle(theme)

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
	muted := mutedStyle(theme)

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
