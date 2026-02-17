package tui2

import "github.com/charmbracelet/lipgloss"

// Theme holds all colors used by the TUI. Views reference theme fields,
// never raw color values.
type Theme struct {
	// Core
	Bg       lipgloss.Color // main background
	BgAlt    lipgloss.Color // elevated/secondary background
	Fg       lipgloss.Color // default text
	FgDim    lipgloss.Color // de-emphasized text (labels, separators, hints)
	FgBright lipgloss.Color // emphasized text (values, container names)
	White    lipgloss.Color // maximum emphasis (selected items, bold headings)
	Border   lipgloss.Color // dividers, separators

	// Semantic
	Accent   lipgloss.Color // focus indicators, selection, interactive elements
	Healthy  lipgloss.Color // running, connected, all clear, status dots
	Warning  lipgloss.Color // high usage, degraded, warn-severity alerts
	Critical lipgloss.Color // exited, unhealthy, crit-severity alerts
	DebugLevel lipgloss.Color // log level color for DEBUG — quieter than InfoLevel
	InfoLevel  lipgloss.Color // log level color for INFO — softer than message text (Fg)

	// Graph-specific
	GraphCPU  lipgloss.Color // CPU sparkline
	GraphMem  lipgloss.Color // memory sparkline
	GraphDisk lipgloss.Color // disk block bar
}

// DefaultTheme returns the Tokyo Night hex theme.
func DefaultTheme() Theme {
	return Theme{
		Bg:       lipgloss.Color("#1a1b26"),
		BgAlt:    lipgloss.Color("#16161e"),
		Fg:       lipgloss.Color("#a9b1d6"),
		FgDim:    lipgloss.Color("#3b4261"),
		FgBright: lipgloss.Color("#c0caf5"),
		White:    lipgloss.Color("#e0e4f0"),
		Border:   lipgloss.Color("#292e42"),
		Accent:   lipgloss.Color("#7aa2f7"),
		Healthy:  lipgloss.Color("#9ece6a"),
		Warning:  lipgloss.Color("#e0af68"),
		Critical: lipgloss.Color("#f7768e"),
		DebugLevel: lipgloss.Color("#414769"),
		InfoLevel:  lipgloss.Color("#505a85"),
		GraphCPU:   lipgloss.Color("#7dcfff"),
		GraphMem: lipgloss.Color("#bb9af7"),
		GraphDisk: lipgloss.Color("#9ece6a"),
	}
}

// TerminalTheme returns a theme using ANSI colors that inherits terminal background.
func TerminalTheme() Theme {
	return Theme{
		Bg:       lipgloss.Color(""),
		BgAlt:    lipgloss.Color(""),
		Fg:       lipgloss.Color("7"),
		FgDim:    lipgloss.Color("8"),
		FgBright: lipgloss.Color("15"),
		White:    lipgloss.Color("15"),
		Border:   lipgloss.Color("8"),
		Accent:   lipgloss.Color("4"),
		Healthy:  lipgloss.Color("2"),
		Warning:  lipgloss.Color("3"),
		Critical: lipgloss.Color("1"),
		DebugLevel: lipgloss.Color("8"),
		InfoLevel:  lipgloss.Color("7"),
		GraphCPU:   lipgloss.Color("6"),
		GraphMem: lipgloss.Color("5"),
		GraphDisk: lipgloss.Color("2"),
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

// hasHealthcheck returns true when the health string indicates a Docker
// healthcheck is configured. Only "healthy", "unhealthy", and "starting"
// are meaningful values.
func hasHealthcheck(health string) bool {
	return health == "healthy" || health == "unhealthy" || health == "starting"
}
