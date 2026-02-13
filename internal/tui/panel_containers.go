package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

// renderContainerPanel renders the container list with grouping and cursor.
// When focused is true, the cursor row uses Reverse; when false, accent foreground.
func renderContainerPanel(groups []containerGroup, collapsed map[string]bool, cursor int, alerts map[int64]*protocol.AlertEvent, contInfo []protocol.ContainerInfo, rc RenderContext, focused bool) string {
	innerH := rc.Height - 2
	if innerH < 1 {
		innerH = 1
	}
	innerW := rc.Width - 2

	// Build set of container IDs with active alerts.
	alertIDs := make(map[string]bool)
	for _, a := range alerts {
		if idx := strings.LastIndex(a.InstanceKey, ":"); idx >= 0 {
			alertIDs[a.InstanceKey[idx+1:]] = true
		}
	}

	// Build tracked state lookup from contInfo.
	trackedState := make(map[string]bool, len(contInfo))
	for _, ci := range contInfo {
		trackedState[ci.ID] = ci.Tracked
	}

	// Fixed-width columns: state(1) + sp(1) + alert(2) + 2sp + health(1) + 2sp + cpu(6) + 2sp + mem(6) + 2sp + uptime(7) + sp(1) + restart(3) = ~34
	fixedCols := 34
	nameW := innerW - fixedCols - 2 // 2 for leading/trailing space
	if nameW < 8 {
		nameW = 8
	}

	theme := rc.Theme
	muted := lipgloss.NewStyle().Foreground(theme.Muted)

	// Build column header (pinned above scroll region).
	headerStats := fmt.Sprintf("%6s  %6s  %-7s", "CPU", "MEM", "UPTIME")
	headerLine := muted.Render(fmt.Sprintf(" %s %s%-*s  %s  %s %s", " ", "  ", nameW, "", "H", headerStats, " ↻"))

	var lines []string
	pos := 0
	for _, g := range groups {
		// Check if all containers in group are untracked.
		allUntracked := len(g.containers) > 0
		for _, c := range g.containers {
			if tracked, ok := trackedState[c.ID]; ok && tracked {
				allUntracked = false
				break
			}
		}

		// Group header: "myapp ────── 4/4 running"
		runLabel := fmt.Sprintf(" %d/%d running", g.running, len(g.containers))
		if allUntracked {
			runLabel += " [not tracked]"
		}
		fillW := innerW - lipgloss.Width(g.name) - lipgloss.Width(runLabel) - 4
		if fillW < 1 {
			fillW = 1
		}
		fill := strings.Repeat("─", fillW)
		headerLine := fmt.Sprintf(" %s %s %s",
			lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render(g.name),
			muted.Render(fill),
			muted.Render(runLabel))

		if pos == cursor {
			if focused {
				headerLine = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(headerLine), innerW))
			} else {
				headerLine = lipgloss.NewStyle().Foreground(theme.Accent).Render(Truncate(stripANSI(headerLine), innerW))
			}
		}
		lines = append(lines, TruncateStyled(headerLine, innerW))
		pos++

		if collapsed[g.name] {
			continue
		}

		// Container rows.
		for _, c := range g.containers {
			tracked := true
			if t, ok := trackedState[c.ID]; ok {
				tracked = t
			}

			indicator := theme.StateIndicator(c.State)
			alertInd := "  "
			if alertIDs[c.ID] {
				alertInd = lipgloss.NewStyle().Foreground(theme.Critical).Render("▲ ")
			}
			name := Truncate(stripANSI(c.Name), nameW)
			health := theme.HealthIndicator(c.Health)
			uptime := formatContainerUptime(c.State, c.StartedAt, c.ExitCode)
			restarts := formatRestarts(c.RestartCount, theme)

			var stats string
			isMuted := !tracked
			if tracked && c.State == "running" {
				stats = fmt.Sprintf("%5.1f%%  %6s  %-7s", c.CPUPercent, FormatBytes(c.MemUsage), Truncate(uptime, 7))
			} else {
				stats = fmt.Sprintf("%6s  %6s  %-7s", "—", "—", Truncate(uptime, 7))
			}
			row := fmt.Sprintf(" %s %s%-*s  %s  %s %s", indicator, alertInd, nameW, name, health, stats, restarts)
			if pos == cursor {
				if focused {
					row = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(row), innerW))
				} else {
					row = lipgloss.NewStyle().Foreground(theme.Accent).Render(Truncate(stripANSI(row), innerW))
				}
			} else if isMuted {
				row = muted.Render(stripANSI(row))
			}
			lines = append(lines, TruncateStyled(row, innerW))
			pos++
		}
	}

	// Scroll: reserve 1 line for the pinned header.
	scrollH := innerH - 1
	if scrollH < 1 {
		scrollH = 1
	}
	if len(lines) > scrollH {
		start := cursor - scrollH/2
		if start < 0 {
			start = 0
		}
		if start+scrollH > len(lines) {
			start = len(lines) - scrollH
		}
		lines = lines[start : start+scrollH]
	}
	lines = append([]string{headerLine}, lines...)

	return Box("Containers", strings.Join(lines, "\n"), rc.Width, rc.Height, theme, focused)
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
