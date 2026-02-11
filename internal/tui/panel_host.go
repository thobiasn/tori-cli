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

	// Braille graph with CPU% embedded in the last line and grid labels on the right.
	if len(cpuHistory) > 0 {
		graphW := innerW - len(cpuVal) - 2 // space + value + leading space
		if graphW < 10 {
			graphW = 10
		}
		gridPcts := []float64{0, 50, 80, 100}
		graph := GraphWithGrid(cpuHistory, graphW, graphRows, 100, gridPcts, theme)
		graphLines := strings.Split(graph, "\n")

		// Map grid percentages to braille row indices for labels.
		gridLabels := make(map[int]string)
		for _, pct := range []float64{100, 50, 80} {
			row := int(float64(graphRows-1) * (1.0 - pct/100.0))
			if _, taken := gridLabels[row]; !taken {
				gridLabels[row] = fmt.Sprintf("%3.0f", pct)
			}
		}

		muted := lipgloss.NewStyle().Foreground(theme.Muted)
		labelW := len(cpuVal) + 1 // reuse the right margin
		for i, gl := range graphLines {
			if i == len(graphLines)-1 {
				lines = append(lines, " "+gl+" "+cpuVal)
			} else if label, ok := gridLabels[i]; ok {
				pad := labelW - len(label)
				if pad < 1 {
					pad = 1
				}
				lines = append(lines, " "+gl+strings.Repeat(" ", pad)+muted.Render(label))
			} else {
				lines = append(lines, " "+gl+strings.Repeat(" ", labelW))
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

// memDivider renders a btop-style divider: ─Label:──────value─
func memDivider(label, value string, width int, labelColor lipgloss.Color, theme *Theme) string {
	// "─" + label + ":─" + fill + value + "─"
	labelStr := label + ":"
	labelLen := len(labelStr)
	valueLen := len(value)
	fillLen := width - 2 - labelLen - valueLen // 2 for leading and trailing ─
	if fillLen < 1 {
		fillLen = 1
	}
	muted := lipgloss.NewStyle().Foreground(theme.Muted)
	styledLabel := lipgloss.NewStyle().Foreground(labelColor).Render(labelStr)
	return muted.Render("─") + styledLabel + muted.Render(strings.Repeat("─", fillLen)+value+"─")
}

// renderMemPanel renders the memory panel with a grid-backed braille graph (like CPU).
func renderMemPanel(host *protocol.HostMetrics, usedHistory []float64, width, height int, theme *Theme) string {
	if host == nil {
		return Box("Memory", "  waiting for data...", width, height, theme)
	}

	innerW := width - 2
	// Layout: borders (2) + info line (1) = 3 fixed lines.
	graphRows := height - 3
	if graphRows < 1 {
		graphRows = 1
	}

	memVal := fmt.Sprintf("%5.1f%%", host.MemPercent)

	var lines []string

	if len(usedHistory) > 0 {
		graphW := innerW - len(memVal) - 2
		if graphW < 10 {
			graphW = 10
		}
		gridPcts := []float64{0, 50, 80, 100}
		graph := GraphWithGrid(usedHistory, graphW, graphRows, 100, gridPcts, theme)
		graphLines := strings.Split(graph, "\n")

		// Map grid percentages to braille row indices for labels.
		gridLabels := make(map[int]string)
		for _, pct := range []float64{100, 50, 80} {
			row := int(float64(graphRows-1) * (1.0 - pct/100.0))
			if _, taken := gridLabels[row]; !taken {
				gridLabels[row] = fmt.Sprintf("%3.0f", pct)
			}
		}

		muted := lipgloss.NewStyle().Foreground(theme.Muted)
		labelW := len(memVal) + 1
		for i, gl := range graphLines {
			if i == len(graphLines)-1 {
				lines = append(lines, " "+gl+" "+memVal)
			} else if label, ok := gridLabels[i]; ok {
				pad := labelW - len(label)
				if pad < 1 {
					pad = 1
				}
				lines = append(lines, " "+gl+strings.Repeat(" ", pad)+muted.Render(label))
			} else {
				lines = append(lines, " "+gl+strings.Repeat(" ", labelW))
			}
		}
	} else {
		lines = append(lines, fmt.Sprintf(" Mem %s", memVal))
	}

	// Bottom info: used/total.
	usedStr := fmt.Sprintf(" Used: %s / %s", FormatBytes(host.MemUsed), FormatBytes(host.MemTotal))
	lines = append(lines, usedStr)

	return Box("Memory", strings.Join(lines, "\n"), width, height, theme)
}

// renderDiskPanel renders a btop-style disk panel with per-mountpoint Used/Free bars
// and swap usage if present.
func renderDiskPanel(disks []protocol.DiskMetrics, swapTotal, swapUsed uint64, width, height int, theme *Theme) string {
	if len(disks) == 0 && swapTotal == 0 {
		return Box("Disks", "  no disks", width, height, theme)
	}

	innerW := width - 2

	addMetric := func(lines []string, label string, pct float64, val string, color lipgloss.Color) []string {
		pctStr := fmt.Sprintf("%3.0f%%", pct)
		prefix := " " + label + ":" + pctStr + " "
		suffix := " " + val
		barW := innerW - lipgloss.Width(prefix) - len(suffix)
		if barW < 4 {
			barW = 4
		}
		bar := ProgressBarFixedColor(pct, barW, color, theme)
		return append(lines, prefix+bar+suffix)
	}

	var lines []string
	for _, d := range disks {
		lines = append(lines, memDivider(d.Mountpoint, FormatBytes(d.Total), innerW, theme.Accent, theme))
		lines = addMetric(lines, "Used", d.Percent, FormatBytes(d.Used), theme.MemUsed)
		lines = addMetric(lines, "Free", 100-d.Percent, FormatBytes(d.Free), theme.MemFree)
	}

	if swapTotal > 0 {
		swapPct := float64(swapUsed) / float64(swapTotal) * 100
		freePct := 100 - swapPct
		lines = append(lines, memDivider("Swap", FormatBytes(swapTotal), innerW, theme.Warning, theme))
		lines = addMetric(lines, "Used", swapPct, FormatBytes(swapUsed), theme.MemUsed)
		lines = addMetric(lines, "Free", freePct, FormatBytes(swapTotal-swapUsed), theme.MemFree)
	}

	return Box("Disks", strings.Join(lines, "\n"), width, height, theme)
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
