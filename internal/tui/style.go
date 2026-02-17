package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Style constructors — eliminate repeated inline lipgloss.NewStyle().Foreground() calls.

func mutedStyle(t *Theme) lipgloss.Style  { return lipgloss.NewStyle().Foreground(t.FgDim) }
func accentStyle(t *Theme) lipgloss.Style { return lipgloss.NewStyle().Foreground(t.Accent) }
func fgStyle(t *Theme) lipgloss.Style     { return lipgloss.NewStyle().Foreground(t.Fg) }

// styledSep returns a " · " separator with a muted dot.
func styledSep(t *Theme) string {
	return " " + mutedStyle(t).Render("·") + " "
}

// birdIcon returns the styled bird based on blink state.
func birdIcon(blink bool, t *Theme) string {
	bird := "—(•)>"
	if blink {
		bird = "—(-)>"
	}
	return accentStyle(t).Render(bird)
}

// cursorRow highlights a row as the cursor selection using Reverse.
func cursorRow(row string, w int) string {
	return lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(row), w))
}

// healthLabel returns "icon health" styled with the appropriate color,
// or a dim "~ no check" / "~ no checks" when no healthcheck is configured.
func healthLabel(health string, plural bool, theme *Theme) string {
	if !hasHealthcheck(health) {
		label := "~ no check"
		if plural {
			label = "~ no checks"
		}
		return mutedStyle(theme).Render(label)
	}
	hColor := theme.Healthy
	if health != "healthy" {
		hColor = theme.Critical
		if health == "starting" {
			hColor = theme.Warning
		}
	}
	return healthIcon(health, theme) + " " + lipgloss.NewStyle().Foreground(hColor).Render(health)
}

// pageFrame centers content horizontally (if terminal is wider than contentW)
// and pads/trims vertically to fill the terminal height.
func pageFrame(content string, contentW, termW, termH int) string {
	if termW > contentW {
		padLeft := (termW - contentW) / 2
		padding := strings.Repeat(" ", padLeft)
		var centered []string
		for _, line := range strings.Split(content, "\n") {
			centered = append(centered, padding+line)
		}
		content = strings.Join(centered, "\n")
	}

	lines := strings.Split(content, "\n")
	for len(lines) < termH {
		lines = append(lines, "")
	}
	if len(lines) > termH {
		lines = lines[:termH]
	}
	return strings.Join(lines, "\n")
}

// helpBinding describes a key-label pair for the help bar.
type helpBinding struct{ Key, Label string }

// renderHelpBar renders a centered help bar from key-label bindings.
func renderHelpBar(bindings []helpBinding, w int, t *Theme) string {
	dim := mutedStyle(t)
	bright := fgStyle(t)

	var parts []string
	for _, b := range bindings {
		parts = append(parts, bright.Render(b.Key)+" "+dim.Render(b.Label))
	}
	return centerText(strings.Join(parts, "  "), w)
}
