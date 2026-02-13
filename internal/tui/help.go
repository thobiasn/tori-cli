package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var dashboardHelp = `
 Navigation
   Tab          Focus servers/alerts/containers
   j/k, ↑/↓    Move cursor
   Enter        Expand alert / open detail
   Space        Collapse/expand group
   t            Toggle tracking
   +/-          Zoom time window
   1-2          Switch view
   ?            Toggle help
   q            Quit`

var alertViewHelp = `
 Navigation
   j/k, ↑/↓    Move cursor
   Tab          Toggle Alerts / Rules
   r            Toggle resolved section
   a            Acknowledge alert
   s            Silence alert / rule
   Enter        Expand alert details
   Esc          Clear selection
   1-2          Switch view
   ?            Toggle help
   q            Quit`

var detailViewHelp = `
 Navigation
   j/k, ↑/↓    Move cursor / scroll
   Enter        Expand/collapse line
   g            Cycle project filter
   s            Cycle stream (all/stdout/stderr)
   f            Open filter
   +/-          Zoom time window
   Esc          Clear filters / back
   Tab, 1-2     Back to dashboard
   ?            Toggle help
   q            Quit`

// helpOverlay renders a centered help box on top of the current view.
func helpOverlay(active view, width, height int, theme *Theme) string {
	var text string
	switch active {
	case viewDashboard:
		text = dashboardHelp
	case viewAlerts:
		text = alertViewHelp
	case viewDetail:
		text = detailViewHelp
	}

	lines := strings.Split(strings.TrimSpace(text), "\n")

	muted := lipgloss.NewStyle().Foreground(theme.Muted)
	lines = append(lines, "")
	lines = append(lines, " "+muted.Render("Esc Close"))

	boxW := 42
	if boxW > width-4 {
		boxW = width - 4
	}
	boxH := len(lines) + 2
	if boxH > height-2 {
		boxH = height - 2
	}

	content := strings.Join(lines, "\n")
	return Box("Help", content, boxW, boxH, theme)
}
