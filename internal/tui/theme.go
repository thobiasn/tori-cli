package tui

import "github.com/charmbracelet/lipgloss"

// Theme holds all colors used by the TUI. Views reference theme fields,
// never raw color values.
type Theme struct {
	Critical lipgloss.Color // red
	Warning  lipgloss.Color // yellow
	Healthy  lipgloss.Color // green
	Accent   lipgloss.Color // cyan
	Muted    lipgloss.Color // gray
}

// DefaultTheme returns the default color theme using standard terminal colors.
func DefaultTheme() Theme {
	return Theme{
		Critical: lipgloss.Color("9"),
		Warning:  lipgloss.Color("11"),
		Healthy:  lipgloss.Color("10"),
		Accent:   lipgloss.Color("14"),
		Muted:    lipgloss.Color("8"),
	}
}

// UsageColor returns green/yellow/red based on a usage percentage.
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
		return t.Muted
	}
}

// StateIndicator returns a colored circle indicator for a container state.
// Active states use ● (filled), inactive use ○ (empty).
func (t Theme) StateIndicator(state string) string {
	color := t.StateColor(state)
	style := lipgloss.NewStyle().Foreground(color)
	switch state {
	case "running", "restarting", "unhealthy":
		return style.Render("●")
	default:
		return style.Render("○")
	}
}
