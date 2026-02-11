package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/rook/internal/protocol"
)

// renderSelectedPanel renders the selected container/group detail panel.
func renderSelectedPanel(a *App, s *Session, width, height int, theme *Theme) string {
	group, idx := cursorContainerMetrics(s.Dash.groups, s.Dash.collapsed, s.Dash.cursor)
	if group == nil {
		return Box("Selected", "  Move cursor to a container", width, height, theme)
	}

	// Cursor on group header — show group summary.
	if idx < 0 {
		return renderGroupSummary(s, group, width, height, theme)
	}

	c := &group.containers[idx]
	return renderContainerSelected(s, c, width, height, theme)
}

func renderGroupSummary(s *Session, g *containerGroup, width, height int, theme *Theme) string {
	innerW := width - 2
	innerH := height - 2
	var totalCPU float64
	var totalMem uint64
	for _, c := range g.containers {
		totalCPU += c.CPUPercent
		totalMem += c.MemUsage
	}

	ids := make([]string, len(g.containers))
	for i, c := range g.containers {
		ids[i] = c.ID
	}

	// Charts take 1/2 of inner height.
	chartBudget := innerH / 2
	var totalDisk uint64
	for _, c := range g.containers {
		totalDisk += c.DiskUsage
	}
	hasDisk := totalDisk > 0
	diskH := 0
	if hasDisk {
		diskH = 3
	}
	graphH := chartBudget - diskH
	if graphH < 5 {
		graphH = 5
	}

	// CPU + MEM side-by-side inner boxes (auto-scaled: aggregated values).
	leftW := innerW / 2
	rightW := innerW - leftW
	graphRows := graphH - 2
	if graphRows < 1 {
		graphRows = 1
	}

	cpuVal := fmt.Sprintf("%5.1f%%", totalCPU)
	cpuAgg := aggregateHistory(s.CPUHistory, ids)
	var cpuContent string
	if len(cpuAgg) > 0 {
		cpuContent = strings.Join(autoGridGraph(cpuAgg, cpuVal, leftW-2, graphRows, theme), "\n")
	} else {
		cpuContent = fmt.Sprintf(" CPU: %s", cpuVal)
	}

	memVal := FormatBytes(totalMem)
	memAgg := aggregateHistory(s.MemHistory, ids)
	var memContent string
	if len(memAgg) > 0 {
		memContent = strings.Join(autoGridGraph(memAgg, memVal, rightW-2, graphRows, theme), "\n")
	} else {
		memContent = fmt.Sprintf(" MEM: %s", memVal)
	}

	graphs := lipgloss.JoinHorizontal(lipgloss.Top,
		Box("CPU", cpuContent, leftW, graphH, theme),
		Box("Memory", memContent, rightW, graphH, theme))

	var lines []string
	lines = append(lines, strings.Split(graphs, "\n")...)

	if hasDisk {
		lines = append(lines, strings.Split(renderGroupDiskBox(totalDisk, innerW, diskH, theme), "\n")...)
	}

	lines = append(lines, "")

	for _, c := range g.containers {
		indicator := theme.StateIndicator(c.State)
		var stats string
		if c.State == "running" {
			stats = fmt.Sprintf("%5.1f%%  %5s", c.CPUPercent, FormatBytes(c.MemUsage))
		} else {
			stats = "   —      —"
		}
		lines = append(lines, fmt.Sprintf(" %s %-16s %s", indicator, Truncate(c.Name, 16), stats))
	}

	title := fmt.Sprintf("Group: %s ── %d/%d running", g.name, g.running, len(g.containers))
	return Box(title, strings.Join(lines, "\n"), width, height, theme)
}

// aggregateHistory sums per-index values across multiple container histories (right-aligned).
func aggregateHistory(histories map[string]*RingBuffer[float64], ids []string) []float64 {
	// Find max length.
	maxLen := 0
	for _, id := range ids {
		if buf, ok := histories[id]; ok {
			d := buf.Data()
			if len(d) > maxLen {
				maxLen = len(d)
			}
		}
	}
	if maxLen == 0 {
		return nil
	}

	agg := make([]float64, maxLen)
	for _, id := range ids {
		buf, ok := histories[id]
		if !ok {
			continue
		}
		d := buf.Data()
		offset := maxLen - len(d) // right-align
		for i, v := range d {
			agg[offset+i] += v
		}
	}
	return agg
}

func renderContainerSelected(s *Session, c *protocol.ContainerMetrics, width, height int, theme *Theme) string {
	innerW := width - 2
	innerH := height - 2

	// Charts take 1/2 of inner height.
	chartBudget := innerH / 2
	hasDisk := c.DiskUsage > 0
	diskH := 0
	if hasDisk {
		diskH = 3
	}
	graphH := chartBudget - diskH
	if graphH < 5 {
		graphH = 5
	}

	// CPU + MEM side-by-side inner boxes.
	leftW := innerW / 2
	rightW := innerW - leftW
	graphRows := graphH - 2 // inner box borders
	if graphRows < 1 {
		graphRows = 1
	}

	// CPU: use limit-based scale if container has an explicit CPU limit, auto-scale otherwise.
	hasCPULimit := c.CPULimit > 0
	cpuVal := fmt.Sprintf("%5.1f%%", c.CPUPercent)
	cpuData := historyData(s.CPUHistory, c.ID)
	var cpuContent string
	if len(cpuData) > 0 {
		if hasCPULimit {
			cpuContent = strings.Join(limitGridGraph(cpuData, cpuVal, leftW-2, graphRows, c.CPULimit,
				formatAutoLabel(c.CPULimit)+"%", formatAutoLabel(c.CPULimit/2)+"%", theme), "\n")
		} else {
			cpuContent = strings.Join(autoGridGraph(cpuData, cpuVal, leftW-2, graphRows, theme), "\n")
		}
	} else {
		cpuContent = fmt.Sprintf(" CPU: %s", cpuVal)
	}

	// MEM: use limit-based scale if container has an explicit limit, auto-scale otherwise.
	// Docker sets MemLimit to host total when no limit is configured.
	hasMemLimit := c.MemLimit > 0 && s.Host != nil && c.MemLimit < s.Host.MemTotal
	memVal := fmt.Sprintf("%s / %s", FormatBytes(c.MemUsage), FormatBytes(c.MemLimit))
	memData := historyData(s.MemHistory, c.ID)
	var memContent string
	if len(memData) > 0 {
		if hasMemLimit {
			memContent = strings.Join(limitGridGraph(memData, memVal, rightW-2, graphRows, 100,
				FormatBytes(c.MemLimit), FormatBytes(c.MemLimit/2), theme), "\n")
		} else {
			memContent = strings.Join(autoGridGraph(memData, memVal, rightW-2, graphRows, theme), "\n")
		}
	} else {
		memContent = fmt.Sprintf(" MEM: %s", memVal)
	}

	graphs := lipgloss.JoinHorizontal(lipgloss.Top,
		Box("CPU", cpuContent, leftW, graphH, theme),
		Box("Memory", memContent, rightW, graphH, theme))

	var lines []string
	lines = append(lines, strings.Split(graphs, "\n")...)

	// Disk box.
	if hasDisk {
		lines = append(lines, strings.Split(renderContainerDiskBox(c.DiskUsage, innerW, diskH, theme), "\n")...)
	}

	// Info lines.
	rates := s.Rates.ContainerRates[c.ID]
	rxStyle := lipgloss.NewStyle().Foreground(theme.Healthy)
	txStyle := lipgloss.NewStyle().Foreground(theme.Accent)
	lines = append(lines, fmt.Sprintf(" NET  %s %s  %s %s    BLK  %s %s  %s %s",
		rxStyle.Render("▼"), FormatBytesRate(rates.NetRxRate),
		txStyle.Render("▲"), FormatBytesRate(rates.NetTxRate),
		rxStyle.Render("R"), FormatBytesRate(rates.BlockReadRate),
		txStyle.Render("W"), FormatBytesRate(rates.BlockWriteRate)))
	lines = append(lines, fmt.Sprintf(" PID  %d    %s    HC %s",
		c.PIDs, formatRestarts(c.RestartCount, theme), theme.HealthText(c.Health)))
	uptime := formatContainerUptime(c.State, c.StartedAt, c.ExitCode)
	lines = append(lines, fmt.Sprintf(" IMG  %s    UP %s",
		Truncate(stripANSI(c.Image), innerW-20), uptime))

	// Title with state indicator.
	stateIndicator := theme.StateIndicator(c.State)
	title := "Selected: " + stripANSI(c.Name) + " ── " + stateIndicator + " " + stripANSI(c.State)
	return Box(title, strings.Join(lines, "\n"), width, height, theme)
}
