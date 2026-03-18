package tui

import "testing"

func TestClampNav(t *testing.T) {
	tests := []struct {
		name   string
		cursor int
		delta  int
		length int
		want   int
	}{
		{"move down", 0, 1, 5, 1},
		{"move up", 3, -1, 5, 2},
		{"clamp at bottom", 4, 1, 5, 4},
		{"clamp at top", 0, -1, 5, 0},
		{"half page down", 0, 3, 10, 3},
		{"half page down clamp", 8, 3, 10, 9},
		{"half page up", 5, -3, 10, 2},
		{"half page up clamp", 1, -3, 10, 0},
		{"pure clamp over", 10, 0, 5, 4},
		{"pure clamp under", -1, 0, 5, 0},
		{"pure clamp in range", 2, 0, 5, 2},
		{"empty list", 5, 1, 0, 0},
		{"single item down", 0, 1, 1, 0},
		{"single item up", 0, -1, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.cursor
			clampNav(&c, tt.delta, tt.length)
			if c != tt.want {
				t.Errorf("clampNav(%d, %d, %d) = %d, want %d",
					tt.cursor, tt.delta, tt.length, c, tt.want)
			}
		})
	}
}

func TestHalfPage(t *testing.T) {
	tests := []struct {
		height int
		want   int
	}{
		{0, 1},
		{1, 1},
		{2, 1},
		{3, 1},
		{4, 2},
		{10, 5},
		{25, 12},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			if got := halfPage(tt.height); got != tt.want {
				t.Errorf("halfPage(%d) = %d, want %d", tt.height, got, tt.want)
			}
		})
	}
}
