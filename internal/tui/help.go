package tui

import (
	"strings"
)

var dashboardHelp = `
 Navigation
   Tab          Focus servers/containers
   j/k, ↑/↓    Move cursor
   Space        Collapse/expand group
   Enter        Open container/group detail
   t            Toggle tracking
   +/-          Zoom time window
   1-2          Switch view
   ?            Toggle help
   q            Quit`

var alertViewHelp = `
 Navigation
   j/k, ↑/↓    Move cursor
   a            Acknowledge alert
   s            Silence alert
   Esc          Clear selection
   Tab, 1-2     Switch view
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
