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
	Grid     lipgloss.Color // dark gray for grid lines

	// Graph colors for container CPU/MEM charts.
	CPUGraph lipgloss.Color // green
	MemGraph lipgloss.Color // blue

	// Memory / disk metric colors.
	MemUsed lipgloss.Color // green
	MemFree lipgloss.Color // yellow

	// ContainerPalette is a set of distinct colors for per-container name coloring.
	ContainerPalette []lipgloss.Color
}

// DefaultTheme returns the default color theme using standard terminal colors.
func DefaultTheme() Theme {
	return Theme{
		Critical: lipgloss.Color("9"),
		Warning:  lipgloss.Color("11"),
		Healthy:  lipgloss.Color("10"),
		Accent:   lipgloss.Color("14"),
		Muted:        lipgloss.Color("8"),
		Grid:         lipgloss.Color("240"),
		CPUGraph: lipgloss.Color("10"), // green
		MemGraph: lipgloss.Color("12"), // blue
		MemUsed: lipgloss.Color("10"),
		MemFree: lipgloss.Color("11"),
		ContainerPalette: []lipgloss.Color{
			lipgloss.Color("14"), // cyan
			lipgloss.Color("13"), // magenta
			lipgloss.Color("12"), // blue
			lipgloss.Color("11"), // yellow
			lipgloss.Color("10"), // green
			lipgloss.Color("9"),  // red
			lipgloss.Color("3"),  // dark yellow
			lipgloss.Color("6"),  // dark cyan
		},
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

// HealthIndicator returns a colored symbol for container health status.
func (t Theme) HealthIndicator(health string) string {
	switch health {
	case "healthy":
		return lipgloss.NewStyle().Foreground(t.Healthy).Render("✓")
	case "unhealthy":
		return lipgloss.NewStyle().Foreground(t.Critical).Render("✗")
	case "starting":
		return lipgloss.NewStyle().Foreground(t.Warning).Render("!")
	default:
		return lipgloss.NewStyle().Foreground(t.Muted).Render("–")
	}
}

// HealthText returns a colored health indicator + label (e.g., "✓ healthy", "– none").
func (t Theme) HealthText(health string) string {
	indicator := t.HealthIndicator(health)
	if health == "" {
		return indicator + " none"
	}
	return indicator + " " + health
}

// RestartColor returns a color based on restart count severity.
func (t Theme) RestartColor(count int) lipgloss.Color {
	switch {
	case count >= 3:
		return t.Critical
	case count >= 1:
		return t.Warning
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
