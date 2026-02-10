package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/rook/internal/protocol"
)

// renderSelectedPanel renders the selected container/group detail panel.
func renderSelectedPanel(a *App, width, height int, theme *Theme) string {
	group, idx := cursorContainerMetrics(a.dash.groups, a.dash.collapsed, a.dash.cursor)
	if group == nil {
		return Box("Selected", "  Move cursor to a container", width, height, theme)
	}

	// Cursor on group header — show group summary.
	if idx < 0 {
		return renderGroupSummary(a, group, width, height, theme)
	}

	c := &group.containers[idx]
	return renderContainerSelected(a, c, width, height, theme)
}

func renderGroupSummary(a *App, g *containerGroup, width, height int, theme *Theme) string {
	innerW := width - 2
	var totalCPU float64
	var totalMem uint64
	for _, c := range g.containers {
		totalCPU += c.CPUPercent
		totalMem += c.MemUsage
	}

	var lines []string

	// Aggregate CPU graph.
	graphRows := 2
	labelW := 5
	cpuVal := fmt.Sprintf("%5.1f%%", totalCPU)
	graphW := innerW - labelW - len(cpuVal) - 1
	if graphW < 10 {
		graphW = 10
	}

	ids := make([]string, len(g.containers))
	for i, c := range g.containers {
		ids[i] = c.ID
	}

	cpuAgg := aggregateHistory(a.cpuHistory, ids)
	if len(cpuAgg) > 0 {
		cpuGraph := Graph(cpuAgg, graphW, graphRows, 0, theme)
		for i, gl := range strings.Split(cpuGraph, "\n") {
			if i == 0 {
				lines = append(lines, " CPU "+gl+" "+cpuVal)
			} else {
				lines = append(lines, strings.Repeat(" ", labelW)+gl)
			}
		}
	} else {
		lines = append(lines, fmt.Sprintf(" CPU: %s", cpuVal))
	}

	// Aggregate MEM graph.
	memVal := fmt.Sprintf("%s", FormatBytes(totalMem))
	memGraphW := innerW - labelW - len(memVal) - 1
	if memGraphW < 10 {
		memGraphW = 10
	}
	memAgg := aggregateHistory(a.memHistory, ids)
	if len(memAgg) > 0 {
		memGraph := Graph(memAgg, memGraphW, graphRows, 0, theme)
		for i, gl := range strings.Split(memGraph, "\n") {
			if i == 0 {
				lines = append(lines, " MEM "+gl+" "+memVal)
			} else {
				lines = append(lines, strings.Repeat(" ", labelW)+gl)
			}
		}
	} else {
		lines = append(lines, fmt.Sprintf(" MEM: %s", memVal))
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

func renderContainerSelected(a *App, c *protocol.ContainerMetrics, width, height int, theme *Theme) string {
	innerW := width - 2
	var lines []string

	// CPU + MEM graphs with aligned widths.
	cpuData := historyData(a.cpuHistory, c.ID)
	memData := historyData(a.memHistory, c.ID)
	graphRows := 3
	labelW := 5 // " CPU " / " MEM "
	cpuVal := fmt.Sprintf("%5.1f%%", c.CPUPercent)
	memVal := fmt.Sprintf("%s / %s", FormatBytes(c.MemUsage), FormatBytes(c.MemLimit))
	valW := max(len(cpuVal), len(memVal))
	graphW := innerW - labelW - valW - 1
	if graphW < 10 {
		graphW = 10
	}
	cpuVal = fmt.Sprintf("%*s", valW, cpuVal)
	memVal = fmt.Sprintf("%*s", valW, memVal)

	if len(cpuData) > 0 {
		cpuGraph := Graph(cpuData, graphW, graphRows, 0, theme)
		for i, line := range strings.Split(cpuGraph, "\n") {
			if i == 0 {
				lines = append(lines, " CPU "+line+" "+cpuVal)
			} else {
				lines = append(lines, strings.Repeat(" ", labelW)+line)
			}
		}
	} else {
		lines = append(lines, fmt.Sprintf(" CPU: %s", cpuVal))
	}

	if len(memData) > 0 {
		memGraph := Graph(memData, graphW, graphRows, 0, theme)
		for i, line := range strings.Split(memGraph, "\n") {
			if i == 0 {
				lines = append(lines, " MEM "+line+" "+memVal)
			} else {
				lines = append(lines, strings.Repeat(" ", labelW)+line)
			}
		}
	} else {
		lines = append(lines, fmt.Sprintf(" MEM: %s", memVal))
	}

	lines = append(lines, "")

	// NET/BLK rates.
	rates := a.rates.ContainerRates[c.ID]
	rxStyle := lipgloss.NewStyle().Foreground(theme.Healthy)
	txStyle := lipgloss.NewStyle().Foreground(theme.Accent)
	lines = append(lines, fmt.Sprintf(" NET  %s %s  %s %s",
		rxStyle.Render("▼"), FormatBytesRate(rates.NetRxRate),
		txStyle.Render("▲"), FormatBytesRate(rates.NetTxRate)))
	lines = append(lines, fmt.Sprintf(" BLK  %s %s  %s %s",
		rxStyle.Render("R"), FormatBytesRate(rates.BlockReadRate),
		txStyle.Render("W"), FormatBytesRate(rates.BlockWriteRate)))

	// PID + RESTARTS.
	lines = append(lines, fmt.Sprintf(" PID  %-14d RESTARTS  %s", c.PIDs, formatRestarts(c.RestartCount, theme)))

	// IMG.
	lines = append(lines, fmt.Sprintf(" IMG  %s", Truncate(stripANSI(c.Image), innerW-6)))

	// UP.
	uptime := formatContainerUptime(c.State, c.StartedAt, c.ExitCode)
	lines = append(lines, fmt.Sprintf(" UP   %s", uptime))

	lines = append(lines, "")

	// HC.
	lines = append(lines, fmt.Sprintf(" HC   %s", theme.HealthText(c.Health)))

	// Separator + disk/net context.
	lines = append(lines, lipgloss.NewStyle().Foreground(theme.Muted).Render(" "+strings.Repeat("─", innerW-2)))
	if a.host != nil && len(a.disks) > 0 {
		d := highestUsageDisk(a.disks)
		diskBarW := innerW - 8
		if diskBarW < 10 {
			diskBarW = 10
		}
		lines = append(lines, fmt.Sprintf(" DISK %s", ProgressBar(d.Percent, diskBarW, theme)))
	}
	lines = append(lines, fmt.Sprintf(" NET  %s %s  %s %s",
		rxStyle.Render("▼"), FormatBytesRate(a.rates.NetRxRate),
		txStyle.Render("▲"), FormatBytesRate(a.rates.NetTxRate)))

	// Title with state indicator.
	stateIndicator := theme.StateIndicator(c.State)
	title := "Selected: " + stripANSI(c.Name) + " ── " + stateIndicator + " " + stripANSI(c.State)
	return Box(title, strings.Join(lines, "\n"), width, height, theme)
}
