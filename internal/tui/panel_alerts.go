package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

// renderAlertPanel renders the dashboard alert bar.
func renderAlertPanel(alerts map[int64]*protocol.AlertEvent, width int, theme *Theme, tsFormat string) string {
	if len(alerts) == 0 {
		return Box("Alerts -- all clear", "", width, 3, theme)
	}

	// Sort by fired_at descending.
	sorted := make([]*protocol.AlertEvent, 0, len(alerts))
	for _, a := range alerts {
		sorted = append(sorted, a)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].FiredAt > sorted[j].FiredAt
	})

	innerW := width - 2
	var lines []string
	for _, a := range sorted {
		ts := FormatTimestamp(a.FiredAt, tsFormat)
		sev := severityTag(a.Severity, theme)
		msg := Truncate(a.Message, innerW-25)
		line := fmt.Sprintf(" %s  %s  %-16s %s", sev, ts, Truncate(a.RuleName, 16), msg)
		lines = append(lines, Truncate(line, innerW))
	}

	title := fmt.Sprintf("Alerts (%d)", len(alerts))
	h := len(lines) + 2 // borders
	if h < 3 {
		h = 3
	}
	return Box(title, strings.Join(lines, "\n"), width, h, theme)
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
