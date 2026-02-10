package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderContainerPanel renders the container list with grouping and cursor.
func renderContainerPanel(groups []containerGroup, collapsed map[string]bool, cursor int, width, height int, theme *Theme) string {
	innerH := height - 2
	if innerH < 1 {
		innerH = 1
	}
	innerW := width - 2

	var lines []string
	pos := 0
	for _, g := range groups {
		// Group header.
		headerLabel := g.name
		runLabel := fmt.Sprintf(" %d running", g.running)
		fillW := innerW - lipgloss.Width(headerLabel) - lipgloss.Width(runLabel) - 4
		if fillW < 1 {
			fillW = 1
		}
		fill := strings.Repeat("─", fillW)
		headerLine := fmt.Sprintf(" %s %s %s",
			lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render(headerLabel),
			lipgloss.NewStyle().Foreground(theme.Muted).Render(fill),
			lipgloss.NewStyle().Foreground(theme.Muted).Render(runLabel))

		if pos == cursor {
			headerLine = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(headerLine), innerW))
		}
		lines = append(lines, TruncateStyled(headerLine, innerW))
		pos++

		if collapsed[g.name] {
			continue
		}

		// Container rows.
		for _, c := range g.containers {
			indicator := theme.StateIndicator(c.State)
			name := Truncate(c.Name, 18)

			var stats string
			if c.State == "running" {
				stats = fmt.Sprintf("%-9s %5.1f%%  %6s", c.State, c.CPUPercent, FormatBytes(c.MemUsage))
			} else {
				stats = fmt.Sprintf("%-9s    —       —", c.State)
			}

			row := fmt.Sprintf(" %s %-18s %s", indicator, name, stats)
			if pos == cursor {
				row = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(row), innerW))
			}
			lines = append(lines, TruncateStyled(row, innerW))
			pos++
		}
	}

	// Scroll: if more lines than innerH, show a window around cursor.
	if len(lines) > innerH {
		start := cursor - innerH/2
		if start < 0 {
			start = 0
		}
		if start+innerH > len(lines) {
			start = len(lines) - innerH
		}
		lines = lines[start : start+innerH]
	}

	return Box("Containers", strings.Join(lines, "\n"), width, height, theme)
}

// cursorContainerID resolves the current cursor position to a container ID.
// Returns empty string if cursor is on a group header.
func cursorContainerID(groups []containerGroup, collapsed map[string]bool, cursor int) string {
	pos := 0
	for _, g := range groups {
		if pos == cursor {
			return "" // on group header
		}
		pos++
		if collapsed[g.name] {
			continue
		}
		for _, c := range g.containers {
			if pos == cursor {
				return c.ID
			}
			pos++
		}
	}
	return ""
}

// maxCursorPos returns the maximum valid cursor position.
func maxCursorPos(groups []containerGroup, collapsed map[string]bool) int {
	pos := 0
	for _, g := range groups {
		pos++ // group header
		if !collapsed[g.name] {
			pos += len(g.containers)
		}
	}
	if pos == 0 {
		return 0
	}
	return pos - 1
}

// cursorGroupName returns the group name at the cursor position (if on a header).
func cursorGroupName(groups []containerGroup, collapsed map[string]bool, cursor int) string {
	pos := 0
	for _, g := range groups {
		if pos == cursor {
			return g.name
		}
		pos++
		if !collapsed[g.name] {
			pos += len(g.containers)
		}
	}
	return ""
}
