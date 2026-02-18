package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderSwitcher renders the server switcher modal overlay.
func renderSwitcher(a *App, width, height int) string {
	theme := &a.theme
	muted := mutedStyle(theme)

	modalW := 56
	if modalW > width-4 {
		modalW = width - 4
	}
	innerW := modalW - 2

	var lines []string

	// Welcome header — centered independently.
	accent := lipgloss.NewStyle().Foreground(theme.Accent)
	lines = append(lines, "")
	lines = append(lines, centerText(accent.Render("—(•)>"), innerW))
	lines = append(lines, "")
	lines = append(lines, centerText(lipgloss.NewStyle().Foreground(theme.Fg).Render("Welcome back! Where should we go?"), innerW))
	lines = append(lines, "")

	if len(a.sessionOrder) == 0 {
		// No servers configured — show setup hint.
		lines = append(lines, centerText(muted.Render("No servers configured."), innerW))
		lines = append(lines, "")
		fg := lipgloss.NewStyle().Foreground(theme.Fg)
		lines = append(lines, centerText(muted.Render("Add a server to"), innerW))
		lines = append(lines, centerText(fg.Render("~/.config/tori/config.toml"), innerW))

		tipLine := dialogTips(theme, "esc", "quit")
		tipPad := (innerW - lipgloss.Width(tipLine)) / 2
		if tipPad < 1 {
			tipPad = 1
		}
		lines = append(lines, "")
		lines = append(lines, "")
		lines = append(lines, strings.Repeat(" ", tipPad)+tipLine)

		content := strings.Join(lines, "\n")
		modalH := len(lines) + 2
		if modalH > height-2 {
			modalH = height - 2
		}
		return renderBox("", content, modalW, modalH, theme)
	}

	// Build server rows, then block-center them.
	var rows []string
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
		if name == a.activeSession && sess.ConnState == ConnReady {
			suffix = muted.Render(" (active)")
		}

		row := dot + " " + name + suffix

		if i == a.switcherCursor {
			row = cursorRow(row, innerW-4)
		}

		rows = append(rows, row)
	}

	// Find widest row for block centering.
	maxW := 0
	for _, r := range rows {
		if w := lipgloss.Width(r); w > maxW {
			maxW = w
		}
	}
	pad := (innerW - maxW) / 2
	if pad < 2 {
		pad = 2
	}
	padStr := strings.Repeat(" ", pad)
	for _, r := range rows {
		lines = append(lines, TruncateStyled(padStr+r, innerW))
	}

	// Help line.
	tipLine := dialogTips(theme, "j/k", "navigate", "enter", "connect", "esc", "close")
	tipPad := (innerW - lipgloss.Width(tipLine)) / 2
	if tipPad < 1 {
		tipPad = 1
	}
	lines = append(lines, "")
	lines = append(lines, "")
	lines = append(lines, strings.Repeat(" ", tipPad)+tipLine)

	content := strings.Join(lines, "\n")
	modalH := len(lines) + 2
	if modalH > height-2 {
		modalH = height - 2
	}

	return renderBox("", content, modalW, modalH, theme)
}
