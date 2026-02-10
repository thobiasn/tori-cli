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
		return renderGroupSummary(group, width, height, theme)
	}

	c := &group.containers[idx]
	return renderContainerSelected(a, c, width, height, theme)
}

func renderGroupSummary(g *containerGroup, width, height int, theme *Theme) string {
	var totalCPU float64
	var totalMem uint64
	for _, c := range g.containers {
		totalCPU += c.CPUPercent
		totalMem += c.MemUsage
	}

	var lines []string
	lines = append(lines, fmt.Sprintf(" %d/%d running", g.running, len(g.containers)))
	lines = append(lines, fmt.Sprintf(" CPU: %.1f%%  MEM: %s", totalCPU, FormatBytes(totalMem)))
	lines = append(lines, "")

	for _, c := range g.containers {
		indicator := theme.StateIndicator(c.State)
		var stats string
		if c.State == "running" {
			stats = fmt.Sprintf("%5.1f%%  %5s", c.CPUPercent, FormatBytes(c.MemUsage))
		} else {
			stats = fmt.Sprintf("   —      —")
		}
		lines = append(lines, fmt.Sprintf(" %s %-16s %s", indicator, Truncate(c.Name, 16), stats))
	}

	title := "Group: " + g.name
	return Box(title, strings.Join(lines, "\n"), width, height, theme)
}

func renderContainerSelected(a *App, c *protocol.ContainerMetrics, width, height int, theme *Theme) string {
	innerW := width - 2
	var lines []string

	// State + health.
	stateColor := theme.StateColor(c.State)
	stateStr := lipgloss.NewStyle().Foreground(stateColor).Render(c.State)
	healthStr := theme.HealthIndicator(c.Health)
	lines = append(lines, fmt.Sprintf(" %s %s", stateStr, healthStr))

	// CPU mini graph.
	cpuData := historyData(a.cpuHistory, c.ID)
	graphRows := 2
	graphW := innerW - 10
	if graphW < 10 {
		graphW = 10
	}
	if len(cpuData) > 0 {
		cpuGraph := Graph(cpuData, graphW, graphRows, 100, theme)
		for _, line := range strings.Split(cpuGraph, "\n") {
			lines = append(lines, " CPU "+line)
		}
	}
	lines = append(lines, fmt.Sprintf(" CPU: %5.1f%%", c.CPUPercent))

	// MEM mini graph.
	memData := historyData(a.memHistory, c.ID)
	if len(memData) > 0 {
		memGraph := Graph(memData, graphW, graphRows, 100, theme)
		for _, line := range strings.Split(memGraph, "\n") {
			lines = append(lines, " MEM "+line)
		}
	}
	lines = append(lines, fmt.Sprintf(" MEM: %s / %s", FormatBytes(c.MemUsage), FormatBytes(c.MemLimit)))

	// NET/BLK rates.
	rates := a.rates.ContainerRates[c.ID]
	rxStyle := lipgloss.NewStyle().Foreground(theme.Healthy)
	txStyle := lipgloss.NewStyle().Foreground(theme.Accent)
	lines = append(lines, fmt.Sprintf(" NET %s %s  %s %s",
		rxStyle.Render("▼"), FormatBytesRate(rates.NetRxRate),
		txStyle.Render("▲"), FormatBytesRate(rates.NetTxRate)))
	lines = append(lines, fmt.Sprintf(" BLK %s %s  %s %s",
		rxStyle.Render("R"), FormatBytesRate(rates.BlockReadRate),
		txStyle.Render("W"), FormatBytesRate(rates.BlockWriteRate)))

	// PIDs, restarts.
	lines = append(lines, fmt.Sprintf(" PIDs: %d  Restarts: %s", c.PIDs, formatRestarts(c.RestartCount, theme)))

	// Uptime and image.
	uptime := formatContainerUptime(c.State, c.StartedAt, c.ExitCode)
	lines = append(lines, fmt.Sprintf(" %s  %s", uptime, Truncate(c.Image, innerW-lipgloss.Width(uptime)-4)))

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
	lines = append(lines, fmt.Sprintf(" NET %s %s  %s %s",
		rxStyle.Render("▼"), FormatBytesRate(a.rates.NetRxRate),
		txStyle.Render("▲"), FormatBytesRate(a.rates.NetTxRate)))

	title := "Selected: " + c.Name
	return Box(title, strings.Join(lines, "\n"), width, height, theme)
}
