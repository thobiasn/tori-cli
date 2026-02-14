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

// alertPanelOpts groups the parameters for renderAlertPanel.
type alertPanelOpts struct {
	alerts   map[int64]*protocol.AlertEvent
	width    int
	height   int // if > 0, constrain to this height; otherwise self-size
	theme    *Theme
	tsFormat string
	cursor   int
	focused  bool
}

// renderAlertPanel renders the dashboard alert bar.
func renderAlertPanel(opts alertPanelOpts) string {
	if len(opts.alerts) == 0 {
		h := 3
		if opts.height > 0 {
			h = opts.height
		}
		return Box("Alerts -- all clear", "", opts.width, h, opts.theme)
	}

	sorted := sortedAlerts(opts.alerts)

	// Clamp cursor.
	cursor := opts.cursor
	if cursor >= len(sorted) {
		cursor = len(sorted) - 1
	}
	if cursor < 0 {
		cursor = 0
	}

	innerW := opts.width - 2
	var lines []string
	for i, a := range sorted {
		ts := FormatTimestamp(a.FiredAt, opts.tsFormat)
		sev := severityTag(a.Severity, opts.theme)
		msg := Truncate(a.Message, innerW-25)
		line := fmt.Sprintf(" %s  %s  %-16s %s", sev, ts, Truncate(a.RuleName, 16), msg)
		line = Truncate(line, innerW)
		if opts.focused && i == cursor {
			line = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(line), innerW))
		}
		lines = append(lines, line)
	}

	title := fmt.Sprintf("Alerts (%d)", len(opts.alerts))
	h := len(lines) + 2 // borders
	if h < 3 {
		h = 3
	}
	if opts.height > 0 && h > opts.height {
		h = opts.height
		// Truncate visible lines to fit.
		maxLines := h - 2
		if maxLines < 0 {
			maxLines = 0
		}
		if len(lines) > maxLines {
			lines = lines[:maxLines]
		}
	}
	return Box(title, strings.Join(lines, "\n"), opts.width, h, opts.theme, opts.focused)
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
