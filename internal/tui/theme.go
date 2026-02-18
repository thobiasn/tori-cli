package tui

import "github.com/charmbracelet/lipgloss"

// Theme holds all colors used by the TUI. Views reference theme fields,
// never raw color values.
type Theme struct {
	// Core
	Fg       lipgloss.Color // default text
	FgDim    lipgloss.Color // de-emphasized text (labels, separators, hints)
	FgBright lipgloss.Color // emphasized text (values, container names)
	Border   lipgloss.Color // dividers, separators

	// Semantic
	Accent     lipgloss.Color // focus indicators, selection, interactive elements
	Healthy    lipgloss.Color // running, connected, all clear, status dots
	Warning    lipgloss.Color // high usage, degraded, warn-severity alerts
	Critical   lipgloss.Color // exited, unhealthy, crit-severity alerts
	DebugLevel lipgloss.Color // log level color for DEBUG — quieter than InfoLevel
	InfoLevel  lipgloss.Color // log level color for INFO — softer than message text (Fg)

	// Graph-specific
	GraphCPU lipgloss.Color // CPU sparkline
	GraphMem lipgloss.Color // memory sparkline
}

// TerminalTheme returns a theme using ANSI colors that inherits terminal background.
func TerminalTheme() Theme {
	return Theme{
		Fg:         lipgloss.Color("7"),
		FgDim:      lipgloss.Color("8"),
		FgBright:   lipgloss.Color("15"),
		Border:     lipgloss.Color("8"),
		Accent:     lipgloss.Color("4"),
		Healthy:    lipgloss.Color("2"),
		Warning:    lipgloss.Color("3"),
		Critical:   lipgloss.Color("1"),
		DebugLevel: lipgloss.Color("8"),
		InfoLevel:  lipgloss.Color("7"),
		GraphCPU:   lipgloss.Color("12"),
		GraphMem:   lipgloss.Color("13"),
	}
}

// hostUsageColor returns a severity color for host CPU/memory percentage
// with FgBright as the calm baseline (used on the dashboard sparklines).
func hostUsageColor(percent float64, theme *Theme) lipgloss.Color {
	switch {
	case percent >= 80:
		return theme.Critical
	case percent >= 60:
		return theme.Warning
	default:
		return theme.Fg
	}
}

// UsageColor returns a color graded by CPU/memory percentage.
func (t Theme) UsageColor(percent float64) lipgloss.Color {
	switch {
	case percent >= 80:
		return t.Critical
	case percent >= 60:
		return t.Warning
	default:
		return t.Healthy
	}
}

// StateColor returns a color for a container state string.
func (t Theme) StateColor(state string) lipgloss.Color {
	switch state {
	case "running":
		return t.Healthy
	case "restarting", "unhealthy":
		return t.Warning
	case "exited", "dead":
		return t.Critical
	default:
		return t.FgDim
	}
}

// StatusDotColor returns the color for a container status dot.
// Running containers with an unhealthy/starting healthcheck show Warning;
// running containers without a healthcheck (or healthy) show Healthy.
func (t Theme) StatusDotColor(state, health string) lipgloss.Color {
	if state != "running" {
		return t.StateColor(state)
	}
	if hasHealthcheck(health) && health != "healthy" {
		return t.Warning
	}
	return t.Healthy
}

// containerCPUColor returns a color for CPU percentage based on configured limit.
// No limit = no severity (dim), same logic as memory.
// Has limit: color by percentage of limit.
func containerCPUColor(cpuPct, cpuLimit float64, theme *Theme) lipgloss.Color {
	if cpuLimit == 0 {
		return theme.FgDim
	}
	pctOfLimit := cpuPct / cpuLimit
	switch {
	case pctOfLimit >= 90:
		return theme.Critical
	case pctOfLimit >= 70:
		return theme.Warning
	default:
		return theme.FgDim
	}
}

// containerMemColor returns a color for memory percentage based on configured limit.
func containerMemColor(memPct float64, memLimit uint64, theme *Theme) lipgloss.Color {
	if memLimit == 0 {
		return theme.FgDim
	}
	switch {
	case memPct >= 90:
		return theme.Critical
	case memPct >= 70:
		return theme.Warning
	default:
		return theme.FgDim
	}
}

// detailCPUColor returns a color for CPU on the detail page.
// Same severity thresholds as the dashboard, but Fg replaces FgDim as the calm baseline.
func detailCPUColor(cpuPct, cpuLimit float64, theme *Theme) lipgloss.Color {
	c := containerCPUColor(cpuPct, cpuLimit, theme)
	if c == theme.FgDim {
		return theme.Fg
	}
	return c
}

// detailMemColor returns a color for memory on the detail page.
// Same severity thresholds as the dashboard, but Fg replaces FgDim as the calm baseline.
func detailMemColor(memPct float64, memLimit uint64, theme *Theme) lipgloss.Color {
	c := containerMemColor(memPct, memLimit, theme)
	if c == theme.FgDim {
		return theme.Fg
	}
	return c
}

// diskSeverityColor returns a color for disk usage percentage.
func diskSeverityColor(pct float64, theme *Theme) lipgloss.Color {
	switch {
	case pct >= 90:
		return theme.Critical
	case pct >= 70:
		return theme.Warning
	default:
		return theme.Fg
	}
}

// loadSeverityColor returns a color for load average based on load1 / CPU count.
func loadSeverityColor(load1 float64, cpus int, theme *Theme) lipgloss.Color {
	if cpus <= 0 {
		cpus = 1
	}
	ratio := load1 / float64(cpus)
	switch {
	case ratio > 1.0:
		return theme.Critical
	case ratio >= 0.7:
		return theme.Warning
	default:
		return theme.Fg
	}
}

// colorRank returns a severity rank for ordering: FgDim=0, Fg=1, Warning=2, Critical=3.
func colorRank(c lipgloss.Color, theme *Theme) int {
	switch c {
	case theme.Critical:
		return 3
	case theme.Warning:
		return 2
	case theme.Fg:
		return 1
	default:
		return 0
	}
}

// projectStatColor returns the color for a project's running-count column.
func projectStatColor(g containerGroup, theme *Theme) lipgloss.Color {
	if g.running == 0 {
		return theme.Critical
	}
	if g.running < len(g.containers) {
		return theme.Warning
	}
	for _, c := range g.containers {
		if hasHealthcheck(c.Health) && c.Health != "healthy" {
			return theme.Warning
		}
	}
	return theme.Healthy
}

// hasHealthcheck returns true when the health string indicates a Docker
// healthcheck is configured. Only "healthy", "unhealthy", and "starting"
// are meaningful values.
func hasHealthcheck(health string) bool {
	return health == "healthy" || health == "unhealthy" || health == "starting"
}

// healthIcon returns a styled single-character healthcheck indicator:
// ✓ (green) for healthy, ✗ (red/amber) for unhealthy/starting, ~ (dim) for no check.
func healthIcon(health string, theme *Theme) string {
	if !hasHealthcheck(health) {
		return lipgloss.NewStyle().Foreground(theme.FgDim).Render("~")
	}
	if health == "healthy" {
		return lipgloss.NewStyle().Foreground(theme.Healthy).Render("✓")
	}
	c := theme.Critical
	if health == "starting" {
		c = theme.Warning
	}
	return lipgloss.NewStyle().Foreground(c).Render("✗")
}
