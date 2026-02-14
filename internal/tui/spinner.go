package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Bird spinner frames — a small bird with flapping wings.
var birdFrames = [...]string{" ─(•)>", " ╲(•)>", " ─(•)>", " ╱(•)>"}

const birdTickInterval = 200 * time.Millisecond

// spinnerTickMsg advances the spinner frame.
type spinnerTickMsg struct{}

// spinnerTick returns a tea.Cmd that sends a spinnerTickMsg after the interval.
func spinnerTick() tea.Cmd {
	return tea.Tick(birdTickInterval, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

// SpinnerView returns a formatted "<bird>  label" string with the bird
// frame styled in the theme's Accent color.
func SpinnerView(frame int, label string, theme *Theme) string {
	f := birdFrames[frame%len(birdFrames)]
	bird := lipgloss.NewStyle().Foreground(theme.Accent).Render(f)
	return bird + "  " + label
}

// SpinnerViewCentered returns a spinner view centered within the given
// width and height. Used for loading states inside Box panels.
func SpinnerViewCentered(frame int, label string, theme *Theme, width, height int) string {
	text := SpinnerView(frame, label, theme)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, text)
}
