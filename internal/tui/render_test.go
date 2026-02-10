package tui

import (
	"strings"
	"testing"
)

func TestProgressBar(t *testing.T) {
	theme := DefaultTheme()
	tests := []struct {
		name    string
		percent float64
		width   int
	}{
		{"zero", 0, 30},
		{"half", 50, 30},
		{"high", 80, 30},
		{"full", 100, 30},
		{"narrow", 50, 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ProgressBar(tt.percent, tt.width, &theme)
			if got == "" {
				t.Fatal("empty progress bar")
			}
			if !strings.HasPrefix(got, "[") {
				t.Errorf("should start with [: %q", got)
			}
			if !strings.Contains(got, "%") {
				t.Errorf("should contain %%: %q", got)
			}
		})
	}
}

func TestProgressBarClamps(t *testing.T) {
	theme := DefaultTheme()
	// Negative percent should clamp to 0.
	got := ProgressBar(-10, 30, &theme)
	if !strings.Contains(got, "0.0%") {
		t.Errorf("negative percent not clamped: %q", got)
	}
	// Over 100 should clamp.
	got = ProgressBar(150, 30, &theme)
	if !strings.Contains(got, "100.0%") {
		t.Errorf(">100 percent not clamped: %q", got)
	}
}

func TestSparkline(t *testing.T) {
	theme := DefaultTheme()

	t.Run("basic", func(t *testing.T) {
		data := []float64{0, 25, 50, 75, 100, 75, 50, 25}
		got := Sparkline(data, 4, &theme)
		runes := []rune(stripANSI(got))
		if len(runes) != 4 {
			t.Fatalf("expected 4 braille chars, got %d: %q", len(runes), got)
		}
		// Each rune should be in braille range.
		for i, r := range runes {
			if r < 0x2800 || r > 0x28FF {
				t.Errorf("char %d not braille: %U", i, r)
			}
		}
	})

	t.Run("all zeros", func(t *testing.T) {
		data := []float64{0, 0, 0, 0}
		got := Sparkline(data, 2, &theme)
		runes := []rune(stripANSI(got))
		for _, r := range runes {
			if r != 0x2800 {
				t.Errorf("expected empty braille for zero data, got %U", r)
			}
		}
	})

	t.Run("all max", func(t *testing.T) {
		data := []float64{100, 100, 100, 100}
		got := Sparkline(data, 2, &theme)
		runes := []rune(stripANSI(got))
		// All max should produce full braille dots.
		for _, r := range runes {
			if r == 0x2800 {
				t.Error("expected non-empty braille for max data")
			}
		}
	})

	t.Run("single point", func(t *testing.T) {
		data := []float64{50}
		got := Sparkline(data, 3, &theme)
		runes := []rune(stripANSI(got))
		if len(runes) != 3 {
			t.Fatalf("expected 3 chars (1 data + 2 pad), got %d", len(runes))
		}
	})

	t.Run("width larger than data", func(t *testing.T) {
		data := []float64{10, 20}
		got := Sparkline(data, 5, &theme)
		runes := []rune(stripANSI(got))
		if len(runes) != 5 {
			t.Fatalf("expected 5 chars, got %d", len(runes))
		}
	})

	t.Run("empty data", func(t *testing.T) {
		got := Sparkline(nil, 5, &theme)
		if got != "" {
			t.Errorf("expected empty string for nil data, got %q", got)
		}
	})

	t.Run("zero width", func(t *testing.T) {
		got := Sparkline([]float64{1, 2, 3}, 0, &theme)
		if got != "" {
			t.Errorf("expected empty string for zero width, got %q", got)
		}
	})

	t.Run("exact braille values", func(t *testing.T) {
		// With data [0, 100], width=1: one braille char.
		// Left col height=0, right col height=4.
		// Right bits for 4 rows: 0x80|0x20|0x10|0x08 = 0xB8
		data := []float64{0, 100}
		got := Sparkline(data, 1, &theme)
		runes := []rune(stripANSI(got))
		if len(runes) != 1 {
			t.Fatalf("expected 1 char, got %d", len(runes))
		}
		expected := rune(0x2800 + 0xB8)
		if runes[0] != expected {
			t.Errorf("expected %U, got %U", expected, runes[0])
		}
	})

	t.Run("left only", func(t *testing.T) {
		// data [100], width=1: left col height=4, no right col.
		// Left bits for 4 rows: 0x40|0x04|0x02|0x01 = 0x47
		data := []float64{100}
		got := Sparkline(data, 1, &theme)
		runes := []rune(stripANSI(got))
		if len(runes) != 1 {
			t.Fatalf("expected 1 char, got %d", len(runes))
		}
		expected := rune(0x2800 + 0x47)
		if runes[0] != expected {
			t.Errorf("expected %U, got %U", expected, runes[0])
		}
	})
}

// stripANSI removes ANSI escape sequences for testing rendered output.
func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes uint64
		want  string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1.0K"},
		{1536, "1.5K"},
		{1048576, "1.0M"},
		{1073741824, "1.0G"},
		{3865470566, "3.6G"},
		{1099511627776, "1.0T"},
	}
	for _, tt := range tests {
		got := FormatBytes(tt.bytes)
		if got != tt.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestFormatBytesRate(t *testing.T) {
	tests := []struct {
		rate float64
		want string
	}{
		{0, "0B/s"},
		{500, "500B/s"},
		{1200, "1.2KB/s"},
		{1200000, "1.2MB/s"},
		{1500000000, "1.5GB/s"},
	}
	for _, tt := range tests {
		got := FormatBytesRate(tt.rate)
		if got != tt.want {
			t.Errorf("FormatBytesRate(%.0f) = %q, want %q", tt.rate, got, tt.want)
		}
	}
}

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		seconds float64
		want    string
	}{
		{0, "0m"},
		{300, "5m"},
		{3600, "1h 0m"},
		{7260, "2h 1m"},
		{86400, "1d 0h 0m"},
		{1234567, "14d 6h 56m"},
	}
	for _, tt := range tests {
		got := FormatUptime(tt.seconds)
		if got != tt.want {
			t.Errorf("FormatUptime(%.0f) = %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

func TestFormatTimestamp(t *testing.T) {
	// Use a known timestamp. 1700000000 = 2023-11-14T22:13:20Z
	got := FormatTimestamp(1700000000)
	// Just verify it's in HH:MM:SS format (8 chars with colons).
	if len(got) != 8 || got[2] != ':' || got[5] != ':' {
		t.Errorf("FormatTimestamp(1700000000) = %q, want HH:MM:SS format", got)
	}
}

func TestBox(t *testing.T) {
	theme := DefaultTheme()

	t.Run("basic", func(t *testing.T) {
		got := Box("Title", "hello", 20, 5, &theme)
		lines := strings.Split(got, "\n")
		if len(lines) != 5 {
			t.Fatalf("expected 5 lines, got %d", len(lines))
		}
		if !strings.HasPrefix(lines[0], "╭") {
			t.Error("top border should start with ╭")
		}
		if !strings.HasSuffix(lines[0], "╮") {
			t.Error("top border should end with ╮")
		}
		last := lines[len(lines)-1]
		if !strings.HasPrefix(last, "╰") || !strings.HasSuffix(last, "╯") {
			t.Errorf("bottom border wrong: %q", last)
		}
	})

	t.Run("no title", func(t *testing.T) {
		got := Box("", "content", 15, 4, &theme)
		if !strings.Contains(got, "╭") {
			t.Error("should have top-left corner")
		}
	})

	t.Run("content padding", func(t *testing.T) {
		got := Box("", "x", 10, 5, &theme)
		lines := strings.Split(got, "\n")
		// 5 lines: top border, 3 content, bottom border
		if len(lines) != 5 {
			t.Fatalf("expected 5 lines, got %d", len(lines))
		}
		// First content line should contain x and be padded.
		if !strings.Contains(lines[1], "x") {
			t.Error("first content line should contain x")
		}
	})
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		s      string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 5, "hell…"},
		{"hello", 1, "…"},
		{"hello", 0, ""},
		{"", 5, ""},
		{"ab", 2, "ab"},
		{"abc", 2, "a…"},
	}
	for _, tt := range tests {
		got := Truncate(tt.s, tt.maxLen)
		if got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
		}
	}
}

func TestContainerNameColor(t *testing.T) {
	theme := DefaultTheme()

	// Same name always returns same color.
	c1 := ContainerNameColor("web", &theme)
	c2 := ContainerNameColor("web", &theme)
	if c1 != c2 {
		t.Errorf("same name returned different colors: %v vs %v", c1, c2)
	}

	// Different names may return different colors (not guaranteed but likely).
	c3 := ContainerNameColor("db", &theme)
	_ = c3 // Just verify it doesn't panic.
}
