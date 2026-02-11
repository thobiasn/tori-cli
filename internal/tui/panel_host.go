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

// memHistories holds per-metric history data for the memory panel.
type memHistories struct {
	Used      []float64
	Available []float64
	Cached    []float64
	Free      []float64
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

// renderMemPanel renders the memory panel with btop-style dividers and braille graphs.
func renderMemPanel(host *protocol.HostMetrics, hist memHistories, width, height int, theme *Theme) string {
	if host == nil {
		return Box("Memory", "  waiting for data...", width, height, theme)
	}

	innerW := width - 2

	var lines []string

	// Total: label with value right-aligned, no graph.
	totalVal := FormatBytes(host.MemTotal)
	totalLabel := " Total:"
	totalGap := innerW - len(totalLabel) - len(totalVal)
	if totalGap < 1 {
		totalGap = 1
	}
	lines = append(lines, totalLabel+strings.Repeat(" ", totalGap)+totalVal)

	// Compute percentages.
	available := host.MemFree + host.MemCached
	usedPct := host.MemPercent
	var availPct, cachedPct, freePct float64
	if host.MemTotal > 0 {
		total := float64(host.MemTotal)
		availPct = float64(available) / total * 100
		cachedPct = float64(host.MemCached) / total * 100
		freePct = float64(host.MemFree) / total * 100
	}

	type metricEntry struct {
		label   string
		pct     float64
		val     string
		color   lipgloss.Color
		history []float64
	}
	metrics := []metricEntry{
		{"Used", usedPct, FormatBytes(host.MemUsed), theme.MemUsed, hist.Used},
		{"Available", availPct, FormatBytes(available), theme.MemAvailable, hist.Available},
		{"Cached", cachedPct, FormatBytes(host.MemCached), theme.MemCached, hist.Cached},
		{"Free", freePct, FormatBytes(host.MemFree), theme.MemFree, hist.Free},
	}

	// Fixed lines per metric: divider (1) + percentage (1) = 2, plus graph rows.
	// Total fixed lines: total line (1) + 4 metrics × 2 fixed = 9.
	// Swap adds 2 lines if present.
	fixedLines := 1 + len(metrics)*2
	hasSwap := host.SwapTotal > 0
	if hasSwap {
		fixedLines += 2
	}
	innerH := height - 2 // box borders
	graphBudget := innerH - fixedLines
	if graphBudget < 0 {
		graphBudget = 0
	}
	rowsPerMetric := graphBudget / len(metrics)
	if rowsPerMetric < 1 {
		rowsPerMetric = 1
	}
	// Cap at reasonable height.
	if rowsPerMetric > 4 {
		rowsPerMetric = 4
	}

	graphW := innerW - 1 // 1 char left padding
	if graphW < 4 {
		graphW = 4
	}

	for _, m := range metrics {
		lines = append(lines, memDivider(m.label, m.val, innerW, m.color, theme))
		lines = append(lines, fmt.Sprintf(" %3.0f%%", m.pct))
		if len(m.history) > 0 && graphBudget > 0 {
			graph := GraphFixedColor(m.history, graphW, rowsPerMetric, 100, m.color)
			for _, gl := range strings.Split(graph, "\n") {
				lines = append(lines, " "+gl)
			}
		}
	}

	if hasSwap {
		swapPct := float64(host.SwapUsed) / float64(host.SwapTotal) * 100
		swapVal := FormatBytes(host.SwapUsed) + "/" + FormatBytes(host.SwapTotal)
		lines = append(lines, memDivider("Swap", swapVal, innerW, theme.Warning, theme))
		lines = append(lines, fmt.Sprintf(" %3.0f%%", swapPct))
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
