package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/rook/internal/protocol"
)

// renderLogPanel renders the dashboard log panel showing recent log entries.
func renderLogPanel(logs *RingBuffer[protocol.LogEntryMsg], width, height int, theme *Theme) string {
	innerH := height - 2
	if innerH < 1 {
		innerH = 1
	}
	innerW := width - 2

	data := logs.Data()
	// Take last innerH entries.
	if len(data) > innerH {
		data = data[len(data)-innerH:]
	}

	var lines []string
	for _, entry := range data {
		lines = append(lines, formatLogLine(entry, innerW, theme))
	}

	return Box("Logs", strings.Join(lines, "\n"), width, height, theme)
}

func formatLogLine(entry protocol.LogEntryMsg, width int, theme *Theme) string {
	ts := lipgloss.NewStyle().Foreground(theme.Muted).Render(FormatTimestamp(entry.Timestamp))
	nameColor := ContainerNameColor(entry.ContainerName, theme)
	name := lipgloss.NewStyle().Foreground(nameColor).Render(fmt.Sprintf("%-14s", Truncate(entry.ContainerName, 14)))

	// ts (8) + space (1) + name (14) + space (1) = 24 chars overhead
	msgW := width - 24
	if msgW < 10 {
		msgW = 10
	}
	msg := Truncate(entry.Message, msgW)

	if entry.Stream == "stderr" {
		msg = lipgloss.NewStyle().Foreground(theme.Critical).Render(msg)
	}

	return fmt.Sprintf("%s %s %s", ts, name, msg)
}
