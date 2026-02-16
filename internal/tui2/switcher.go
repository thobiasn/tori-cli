package tui2

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderSwitcher renders the server switcher modal overlay.
func renderSwitcher(a *App, width, height int) string {
	theme := &a.theme
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

	modalW := 40
	if modalW > width-4 {
		modalW = width - 4
	}
	innerW := modalW - 2

	var lines []string
	for i, name := range a.sessionOrder {
		sess := a.sessions[name]

		// Status dot.
		var dotColor lipgloss.Color
		switch sess.ConnState {
		case ConnReady:
			dotColor = theme.Healthy
		case ConnConnecting, ConnSSH:
			dotColor = theme.Warning
		case ConnError:
			dotColor = theme.Critical
		default:
			dotColor = theme.FgDim
		}
		dot := lipgloss.NewStyle().Foreground(dotColor).Render("●")

		// Active marker.
		suffix := ""
		if name == a.activeSession {
			suffix = muted.Render(" (active)")
		}

		row := " " + dot + " " + name + suffix

		if i == a.switcherCursor {
			row = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(row), innerW))
		}

		lines = append(lines, TruncateStyled(row, innerW))
	}

	// Help line.
	lines = append(lines, "")
	lines = append(lines, muted.Render(" j/k navigate  Enter select  Esc close"))

	content := strings.Join(lines, "\n")
	modalH := len(lines) + 2
	if modalH > height-2 {
		modalH = height - 2
	}

	return renderBox("Servers", content, modalW, modalH, theme)
}
