package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/rook/internal/protocol"
)

// graphAxis controls how auto-scaled graph ceilings and labels are computed.
type graphAxis struct {
	ceilFn   func(float64) float64
	labelFn  func(float64) string
	ceilOnly bool // only show ceiling label, skip midpoint
}

var pctAxis = graphAxis{
	ceilFn:  niceMax,
	labelFn: func(v float64) string { return formatAutoLabel(v) + "%" },
}

var bytesAxis = graphAxis{
	ceilFn:   niceMaxBytes,
	labelFn:  func(v float64) string { return FormatBytes(uint64(v)) },
	ceilOnly: true,
}

// gridGraph renders a braille graph with grid lines at 0/50/80/100% and
// muted percentage labels on the right margin. The value string is placed at
// the bottom-right of the last graph row. Returns the rendered lines.
// An optional color overrides the default per-row UsageColor.
func gridGraph(data []float64, value string, innerW, rows int, windowSec int64, theme *Theme, color ...lipgloss.Color) []string {
	graphW := innerW - len(value) - 2
	if graphW < 10 {
		graphW = 10
	}
	gridPcts := []float64{0, 50, 80, 100}
	if rows < 6 {
		gridPcts = []float64{0, 100}
	}
	graph := GraphWithGrid(data, graphW, rows, 100, gridPcts, timeMarkers(windowSec), theme, color...)
	graphLines := strings.Split(graph, "\n")

	gridLabels := make(map[int]string)
	if rows >= 6 {
		for _, pct := range []float64{100, 50, 80} {
			row := int(float64(rows-1) * (1.0 - pct/100.0))
			if _, taken := gridLabels[row]; !taken {
				gridLabels[row] = fmt.Sprintf("%3.0f%%", pct)
			}
		}
	} else {
		gridLabels[0] = "100%"
	}

	muted := lipgloss.NewStyle().Foreground(theme.Muted)
	labelW := len(value) + 1
	lines := make([]string, len(graphLines))
	for i, gl := range graphLines {
		if i == len(graphLines)-1 {
			lines[i] = " " + gl + " " + value
		} else if label, ok := gridLabels[i]; ok {
			pad := labelW - len(label)
			if pad < 1 {
				pad = 1
			}
			lines[i] = " " + gl + strings.Repeat(" ", pad) + muted.Render(label)
		} else {
			lines[i] = " " + gl + strings.Repeat(" ", labelW)
		}
	}
	return lines
}

// autoGridGraph renders a braille graph auto-scaled to the observed data range.
// Unlike gridGraph which uses a fixed 0-100 scale, this adapts the Y axis to
// the data's observed maximum, making small variations visible.
func autoGridGraph(data []float64, value string, innerW, rows int, windowSec int64, theme *Theme, color lipgloss.Color, axis graphAxis) []string {
	if len(data) == 0 {
		return nil
	}

	var maxObs float64
	for _, v := range data {
		if v > maxObs {
			maxObs = v
		}
	}
	if maxObs < 0.1 {
		maxObs = 1
	}
	maxVal := axis.ceilFn(maxObs)

	// Use the wider of value/ceiling label to size the right margin.
	ceilLabel := axis.labelFn(maxVal)
	rightW := len(value)
	if len(ceilLabel) > rightW {
		rightW = len(ceilLabel)
	}

	graphW := innerW - rightW - 2
	if graphW < 10 {
		graphW = 10
	}

	gridPcts := []float64{0, 50, 100}
	if rows < 6 {
		gridPcts = []float64{0, 100}
	}
	graph := GraphWithGrid(data, graphW, rows, maxVal, gridPcts, timeMarkers(windowSec), theme, color)
	graphLines := strings.Split(graph, "\n")

	// Labels: ceiling at top row, mid at 50% (unless ceilOnly or too short).
	midRow := int(float64(rows-1) * 0.5)
	gridLabels := make(map[int]string)
	gridLabels[0] = axis.labelFn(maxVal)
	if !axis.ceilOnly && midRow > 0 && midRow < rows-1 && rows >= 6 {
		gridLabels[midRow] = axis.labelFn(maxVal / 2)
	}

	muted := lipgloss.NewStyle().Foreground(theme.Muted)
	labelW := rightW + 1
	lines := make([]string, len(graphLines))
	for i, gl := range graphLines {
		if i == len(graphLines)-1 {
			lines[i] = " " + gl + " " + value
		} else if label, ok := gridLabels[i]; ok {
			pad := labelW - len(label)
			if pad < 1 {
				pad = 1
			}
			lines[i] = " " + gl + strings.Repeat(" ", pad) + muted.Render(label)
		} else {
			lines[i] = " " + gl + strings.Repeat(" ", labelW)
		}
	}
	return lines
}

// niceMax rounds a value up to a visually clean ceiling for axis labeling.
func niceMax(v float64) float64 {
	if v <= 0 {
		return 1
	}
	steps := []float64{1, 2, 5, 10, 20, 25, 50, 75, 100, 150, 200, 300, 500, 1000}
	for _, s := range steps {
		if v <= s {
			return s
		}
	}
	// Large values: round up to next 500.
	return float64(int(v/500)+1) * 500
}

// niceMaxBytes rounds a byte value up to a clean ceiling within its unit.
// It normalizes to the appropriate binary unit, applies niceMax on the
// normalized value, then denormalizes back to bytes.
func niceMaxBytes(v float64) float64 {
	if v <= 0 {
		return 1
	}
	switch {
	case v >= 1<<40:
		return niceMax(v/float64(uint64(1)<<40)) * float64(uint64(1)<<40)
	case v >= 1<<30:
		return niceMax(v/float64(uint64(1)<<30)) * float64(uint64(1)<<30)
	case v >= 1<<20:
		return niceMax(v/float64(uint64(1)<<20)) * float64(uint64(1)<<20)
	case v >= 1<<10:
		return niceMax(v/float64(uint64(1)<<10)) * float64(uint64(1)<<10)
	default:
		return niceMax(v)
	}
}

// formatAutoLabel formats a number compactly for auto-scaled axis labels.
func formatAutoLabel(v float64) string {
	if v == float64(int(v)) {
		return fmt.Sprintf("%d", int(v))
	}
	return fmt.Sprintf("%.1f", v)
}

// renderCPUPanel renders the CPU panel with a multi-row braille graph.
// windowLabel is appended to the title (e.g. "CPU · 1h"); empty for Live.
func renderCPUPanel(cpuHistory []float64, host *protocol.HostMetrics, width, height int, theme *Theme, windowLabel string, windowSec int64) string {
	title := "CPU · " + windowLabel
	if host == nil {
		return Box(title, "  waiting for data...", width, height, theme)
	}

	innerW := width - 2
	graphRows := height - 3 // borders (2) + info line (1)
	if graphRows < 1 {
		graphRows = 1
	}

	cpuVal := fmt.Sprintf("%5.1f%%", host.CPUPercent)

	var lines []string

	if len(cpuHistory) > 0 {
		lines = append(lines, gridGraph(cpuHistory, cpuVal, innerW, graphRows, windowSec, theme)...)
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

	return Box(title, strings.Join(lines, "\n"), width, height, theme)
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
// windowLabel is appended to the title; empty for Live.
func renderMemPanel(host *protocol.HostMetrics, usedHistory []float64, width, height int, theme *Theme, windowLabel string, windowSec int64) string {
	title := "Memory · " + windowLabel
	if host == nil {
		return Box(title, "  waiting for data...", width, height, theme)
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
		lines = append(lines, gridGraph(usedHistory, memVal, innerW, graphRows, windowSec, theme, theme.MemGraph)...)
	} else {
		lines = append(lines, fmt.Sprintf(" Mem %s", memVal))
	}

	// Bottom info: used/total.
	usedStr := fmt.Sprintf(" Used: %s / %s", FormatBytes(host.MemUsed), FormatBytes(host.MemTotal))
	lines = append(lines, usedStr)

	return Box(title, strings.Join(lines, "\n"), width, height, theme)
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

// renderContainerDiskBox renders a small bordered disk panel showing container writable layer usage.
func renderContainerDiskBox(diskUsage uint64, width, height int, theme *Theme) string {
	content := " Writable layer: " + FormatBytes(diskUsage)
	return Box("Disk", content, width, height, theme)
}

// renderGroupDiskBox renders a small bordered disk panel showing aggregate container disk usage.
func renderGroupDiskBox(totalDisk uint64, width, height int, theme *Theme) string {
	content := " Writable layers: " + FormatBytes(totalDisk)
	return Box("Disk", content, width, height, theme)
}
