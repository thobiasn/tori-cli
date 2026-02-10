package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestUsageColor(t *testing.T) {
	theme := DefaultTheme()
	tests := []struct {
		percent float64
		want    lipgloss.Color
	}{
		{0, theme.Healthy},
		{30, theme.Healthy},
		{59.9, theme.Healthy},
		{60, theme.Warning},
		{75, theme.Warning},
		{79.9, theme.Warning},
		{80, theme.Critical},
		{95, theme.Critical},
		{100, theme.Critical},
	}
	for _, tt := range tests {
		got := theme.UsageColor(tt.percent)
		if got != tt.want {
			t.Errorf("UsageColor(%.1f) = %v, want %v", tt.percent, got, tt.want)
		}
	}
}

func TestStateIndicator(t *testing.T) {
	theme := DefaultTheme()
	tests := []struct {
		state    string
		wantChar string
	}{
		{"running", "●"},
		{"restarting", "●"},
		{"unhealthy", "●"},
		{"exited", "○"},
		{"dead", "○"},
		{"paused", "○"},
		{"created", "○"},
	}
	for _, tt := range tests {
		got := theme.StateIndicator(tt.state)
		// The indicator is styled, so check it contains the expected character.
		if !containsRune(got, []rune(tt.wantChar)[0]) {
			t.Errorf("StateIndicator(%q) = %q, want to contain %q", tt.state, got, tt.wantChar)
		}
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
