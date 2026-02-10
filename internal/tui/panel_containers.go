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

	// Fixed-width columns: state(1) + space(1) + health(1) + space(1) + cpu(5) + space(1) + mem(5) + space(1) + uptime(7) + space(1) + restart(3) = ~27
	fixedCols := 27
	nameW := innerW - fixedCols - 2 // 2 for leading/trailing space
	if nameW < 8 {
		nameW = 8
	}

	var lines []string
	pos := 0
	for _, g := range groups {
		// Group header: "myapp ────── 4/4 running"
		runLabel := fmt.Sprintf(" %d/%d running", g.running, len(g.containers))
		fillW := innerW - lipgloss.Width(g.name) - lipgloss.Width(runLabel) - 4
		if fillW < 1 {
			fillW = 1
		}
		fill := strings.Repeat("─", fillW)
		headerLine := fmt.Sprintf(" %s %s %s",
			lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render(g.name),
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
			name := Truncate(stripANSI(c.Name), nameW)
			health := theme.HealthIndicator(c.Health)
			uptime := formatContainerUptime(c.State, c.StartedAt, c.ExitCode)
			restarts := formatRestarts(c.RestartCount, theme)

			var stats string
			if c.State == "running" {
				stats = fmt.Sprintf("%5.1f%% %5s %-7s", c.CPUPercent, FormatBytes(c.MemUsage), Truncate(uptime, 7))
			} else {
				stats = fmt.Sprintf("   —      — %-7s", Truncate(uptime, 7))
			}

			row := fmt.Sprintf(" %s %-*s %s %s %s", indicator, nameW, name, health, stats, restarts)
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

// cursorContainerMetrics resolves the cursor to a container's metrics.
// Returns nil and the group name if cursor is on a group header.
func cursorContainerMetrics(groups []containerGroup, collapsed map[string]bool, cursor int) (*containerGroup, int) {
	pos := 0
	for i, g := range groups {
		if pos == cursor {
			return &groups[i], -1
		}
		pos++
		if collapsed[g.name] {
			continue
		}
		for j := range g.containers {
			if pos == cursor {
				return &groups[i], j
			}
			pos++
		}
	}
	return nil, -1
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
