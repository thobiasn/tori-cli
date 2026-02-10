package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var dashboardHelp = `
 Navigation
   j/k, ↑/↓    Move cursor
   Space        Collapse/expand group
   Enter        Open container detail
   l            Open logs for container
   t            Toggle tracking
   Tab, 1-4     Switch view
   ?            Toggle help
   q            Quit`

var logViewHelp = `
 Navigation
   j/k, ↑/↓    Move cursor / scroll
   c            Cycle container filter
   g            Cycle project filter
   s            Cycle stream (all/stdout/stderr)
   /            Text search
   Enter        Expand/collapse line
   Esc          Clear filters
   Tab, 1-4     Switch view
   ?            Toggle help
   q            Quit`

var alertViewHelp = `
 Navigation
   j/k, ↑/↓    Move cursor
   a            Acknowledge alert
   s            Silence alert
   Esc          Clear selection
   Tab, 1-4     Switch view
   ?            Toggle help
   q            Quit`

var detailViewHelp = `
 Navigation
   j/k, ↑/↓    Move cursor / scroll
   Enter        Expand/collapse line
   /            Text search
   s            Cycle stream (all/stdout/stderr)
   r            Restart container
   Esc          Clear filters / back
   Tab, 1-4     Switch view
   ?            Toggle help
   q            Quit`

// helpOverlay renders a centered help box on top of the current view.
func helpOverlay(active view, width, height int, theme *Theme) string {
	var text string
	switch active {
	case viewDashboard:
		text = dashboardHelp
	case viewLogs:
		text = logViewHelp
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
	overlay := Box("Help", content, boxW, boxH, theme)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, overlay)
}
