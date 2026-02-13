package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

// sortedAlerts returns alert events sorted by fired_at descending.
func sortedAlerts(alerts map[int64]*protocol.AlertEvent) []*protocol.AlertEvent {
	sorted := make([]*protocol.AlertEvent, 0, len(alerts))
	for _, a := range alerts {
		sorted = append(sorted, a)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].FiredAt > sorted[j].FiredAt
	})
	return sorted
}

// alertEventToMsg converts a streaming AlertEvent to an AlertMsg for modal display.
func alertEventToMsg(e *protocol.AlertEvent) protocol.AlertMsg {
	return protocol.AlertMsg{
		ID: e.ID, RuleName: e.RuleName, Severity: e.Severity,
		Condition: e.Condition, InstanceKey: e.InstanceKey,
		FiredAt: e.FiredAt, ResolvedAt: e.ResolvedAt, Message: e.Message,
	}
}

// renderAlertPanel renders the dashboard alert bar.
func renderAlertPanel(alerts map[int64]*protocol.AlertEvent, width int, theme *Theme, tsFormat string, cursor int, focused bool) string {
	if len(alerts) == 0 {
		return Box("Alerts -- all clear", "", width, 3, theme)
	}

	sorted := sortedAlerts(alerts)

	// Clamp cursor.
	if cursor >= len(sorted) {
		cursor = len(sorted) - 1
	}
	if cursor < 0 {
		cursor = 0
	}

	innerW := width - 2
	var lines []string
	for i, a := range sorted {
		ts := FormatTimestamp(a.FiredAt, tsFormat)
		sev := severityTag(a.Severity, theme)
		msg := Truncate(a.Message, innerW-25)
		line := fmt.Sprintf(" %s  %s  %-16s %s", sev, ts, Truncate(a.RuleName, 16), msg)
		line = Truncate(line, innerW)
		if focused && i == cursor {
			line = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(line), innerW))
		}
		lines = append(lines, line)
	}

	title := fmt.Sprintf("Alerts (%d)", len(alerts))
	h := len(lines) + 2 // borders
	if h < 3 {
		h = 3
	}
	return Box(title, strings.Join(lines, "\n"), width, h, theme, focused)
}

func severityTag(sev string, theme *Theme) string {
	var color lipgloss.Color
	switch strings.ToLower(sev) {
	case "critical", "crit":
		color = theme.Critical
	case "warning", "warn":
		color = theme.Warning
	default:
		color = theme.Muted
	}
	label := strings.ToUpper(sev)
	if len(label) > 4 {
		label = label[:4]
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render("▲ " + label)
}
