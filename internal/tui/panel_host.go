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
	graphRows := height - 4 // borders (2) + info line (1) + cpu% line (1)
	if graphRows < 1 {
		graphRows = 1
	}

	var lines []string

	// CPU percentage line.
	cpuPct := fmt.Sprintf(" CPU %5.1f%%", host.CPUPercent)
	lines = append(lines, cpuPct)

	// Braille graph.
	if len(cpuHistory) > 0 {
		graph := Graph(cpuHistory, innerW, graphRows, 100, theme)
		lines = append(lines, graph)
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

	// Memory progress bar.
	memLine := fmt.Sprintf(" %s", ProgressBar(host.MemPercent, barW, theme))
	lines = append(lines, memLine)

	// Used / Total.
	usedLine := fmt.Sprintf(" Used: %s / %s", FormatBytes(host.MemUsed), FormatBytes(host.MemTotal))
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

// renderHostPanel is kept for backward compatibility — used by tests.
// It renders the old combined host panel format.
func renderHostPanel(host *protocol.HostMetrics, disks []protocol.DiskMetrics, rates *RateCalc, width, height int, theme *Theme) string {
	if host == nil {
		return Box("Host", "  waiting for data...", width, height, theme)
	}

	innerW := width - 2
	barW := innerW - 7 // label (4) + spaces (3)
	if barW < 10 {
		barW = 10
	}

	var lines []string

	// CPU.
	cpuLine := fmt.Sprintf(" CPU  %s", ProgressBar(host.CPUPercent, barW, theme))
	lines = append(lines, cpuLine)

	// MEM.
	memInfo := fmt.Sprintf("  %s / %s", FormatBytes(host.MemUsed), FormatBytes(host.MemTotal))
	memBarW := barW - lipgloss.Width(memInfo)
	if memBarW < 10 {
		memBarW = 10
		memInfo = ""
	}
	memLine := fmt.Sprintf(" MEM  %s%s", ProgressBar(host.MemPercent, memBarW, theme), memInfo)
	lines = append(lines, memLine)

	// DISK: highest usage mount.
	if len(disks) > 0 {
		d := highestUsageDisk(disks)
		diskInfo := fmt.Sprintf("  %s / %s", FormatBytes(d.Used), FormatBytes(d.Total))
		diskBarW := barW - lipgloss.Width(diskInfo)
		if diskBarW < 10 {
			diskBarW = 10
			diskInfo = ""
		}
		diskLine := fmt.Sprintf(" DISK %s%s", ProgressBar(d.Percent, diskBarW, theme), diskInfo)
		lines = append(lines, diskLine)

		// Show mount point if not root.
		if d.Mountpoint != "/" {
			mount := lipgloss.NewStyle().Foreground(theme.Muted).Render("  " + Truncate(d.Mountpoint, innerW-4))
			lines = append(lines, mount)
		}
	}

	// LOAD.
	loadLine := fmt.Sprintf(" LOAD %.2f %.2f %.2f", host.Load1, host.Load5, host.Load15)
	lines = append(lines, loadLine)

	// UPTIME.
	upLine := fmt.Sprintf(" UP   %s", FormatUptime(host.Uptime))
	lines = append(lines, upLine)

	// Blank line.
	lines = append(lines, "")

	// NET.
	rxStyle := lipgloss.NewStyle().Foreground(theme.Healthy)
	txStyle := lipgloss.NewStyle().Foreground(theme.Accent)
	netLine := fmt.Sprintf(" NET  %s %s %s %s",
		rxStyle.Render("▼"), FormatBytesRate(rates.NetRxRate),
		txStyle.Render("▲"), FormatBytesRate(rates.NetTxRate))
	lines = append(lines, netLine)

	return Box("Host", strings.Join(lines, "\n"), width, height, theme)
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
