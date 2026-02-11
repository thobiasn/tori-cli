package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/rook/internal/protocol"
)

// renderCPUPanel renders the CPU panel with a multi-row braille graph.
func renderCPUPanel(cpuHistory []float64, host *protocol.HostMetrics, width, height int, theme *Theme) string {
	if host == nil {
		return Box("CPU", "  waiting for data...", width, height, theme)
	}

	innerW := width - 2
	graphRows := height - 3 // borders (2) + info line (1)
	if graphRows < 1 {
		graphRows = 1
	}

	cpuVal := fmt.Sprintf("%5.1f%%", host.CPUPercent)

	var lines []string

	// Braille graph with CPU% embedded in the last line.
	if len(cpuHistory) > 0 {
		graphW := innerW - len(cpuVal) - 2 // space + value + leading space
		if graphW < 10 {
			graphW = 10
		}
		graph := Graph(cpuHistory, graphW, graphRows, 0, theme)
		graphLines := strings.Split(graph, "\n")
		pad := strings.Repeat(" ", len(cpuVal)+1)
		for i, gl := range graphLines {
			if i == len(graphLines)-1 {
				lines = append(lines, " "+gl+" "+cpuVal)
			} else {
				lines = append(lines, " "+gl+pad)
			}
		}
	} else {
		lines = append(lines, fmt.Sprintf(" CPU %s", cpuVal))
	}

	// Bottom info: load + uptime.
	loadStr := fmt.Sprintf(" Load: %.2f %.2f %.2f", host.Load1, host.Load5, host.Load15)
	uptimeStr := fmt.Sprintf("Uptime: %s ", FormatUptime(host.Uptime))
	gap := innerW - lipgloss.Width(loadStr) - lipgloss.Width(uptimeStr)
	if gap < 1 {
		gap = 1
	}
	infoLine := loadStr + strings.Repeat(" ", gap) + uptimeStr
	lines = append(lines, infoLine)

	return Box("CPU", strings.Join(lines, "\n"), width, height, theme)
}

// renderMemPanel renders the memory panel with usage details.
func renderMemPanel(host *protocol.HostMetrics, width, height int, theme *Theme) string {
	if host == nil {
		return Box("Memory", "  waiting for data...", width, height, theme)
	}

	innerW := width - 2
	barW := innerW - 2
	if barW < 10 {
		barW = 10
	}

	var lines []string

	// Memory progress bar (no percentage).
	memLine := fmt.Sprintf(" %s", ProgressBarSimple(host.MemPercent, barW, theme))
	lines = append(lines, memLine)

	// Used / Total with percentage.
	usedLine := fmt.Sprintf(" Used: %s / %s  %.1f%%", FormatBytes(host.MemUsed), FormatBytes(host.MemTotal), host.MemPercent)
	lines = append(lines, usedLine)

	// Cached + Free.
	cachedLine := fmt.Sprintf(" Cached: %s  Free: %s", FormatBytes(host.MemCached), FormatBytes(host.MemFree))
	lines = append(lines, cachedLine)

	// Swap.
	if host.SwapTotal > 0 {
		swapLine := fmt.Sprintf(" Swap: %s / %s", FormatBytes(host.SwapUsed), FormatBytes(host.SwapTotal))
		lines = append(lines, swapLine)
	} else {
		lines = append(lines, " Swap: none")
	}

	return Box("Memory", strings.Join(lines, "\n"), width, height, theme)
}

func highestUsageDisk(disks []protocol.DiskMetrics) protocol.DiskMetrics {
	best := disks[0]
	for _, d := range disks[1:] {
		if d.Percent > best.Percent {
			best = d
		}
	}
	return best
}
