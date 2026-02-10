package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/rook/internal/protocol"
)

// renderHostPanel renders the host metrics panel for the dashboard.
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
