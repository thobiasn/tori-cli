package tui

import (
	"strings"
	"testing"
	"time"
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

	t.Run("low value visible", func(t *testing.T) {
		// 0.5 with maxVal auto-scaled should still be visible.
		data := []float64{0.5, 100}
		got := Sparkline(data, 1, &theme)
		runes := []rune(stripANSI(got))
		if len(runes) != 1 {
			t.Fatalf("expected 1 char, got %d", len(runes))
		}
		// Left col (0.5) should have h=1 (clamped), so at least one bit set.
		if runes[0] == rune(0x2800+0xB8) {
			// Only right-col bits — left col wasn't clamped. This shouldn't happen.
		}
		// Just verify it's not blank (left col has at least 1 dot).
		leftBits := runes[0] & 0x47 // bits used by left column
		if leftBits == 0 {
			t.Errorf("low left value should be visible, got %U", runes[0])
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
	got := FormatTimestamp(1700000000, "2006-01-02 15:04:05")
	if !strings.Contains(got, "2023-11-1") && !strings.Contains(got, ":") {
		t.Errorf("FormatTimestamp with date+time format = %q, want date and time", got)
	}
	// Time-only format still works.
	got = FormatTimestamp(1700000000, "15:04:05")
	if len(got) != 8 || got[2] != ':' || got[5] != ':' {
		t.Errorf("FormatTimestamp with time-only = %q, want HH:MM:SS format", got)
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

func TestTruncateStyled(t *testing.T) {
	// Plain text should work like Truncate.
	if got := TruncateStyled("hello", 10); got != "hello" {
		t.Errorf("TruncateStyled plain fits = %q, want hello", got)
	}
	if got := TruncateStyled("hello world", 5); got != "hell…" {
		t.Errorf("TruncateStyled plain truncated = %q, want hell…", got)
	}

	// Styled text that fits visually should be returned as-is.
	styled := "\x1b[31mhello\x1b[0m" // "hello" in red ANSI
	if got := TruncateStyled(styled, 10); got != styled {
		t.Errorf("TruncateStyled styled fits = %q, want original", got)
	}

	// Zero maxLen.
	if got := TruncateStyled("hello", 0); got != "" {
		t.Errorf("TruncateStyled zero = %q, want empty", got)
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"\x1b[31mred\x1b[0m", "red"},
		{"\x1b[1;32mbold green\x1b[0m text", "bold green text"},
		{"", ""},
	}
	for _, tt := range tests {
		got := stripANSI(tt.input)
		if got != tt.want {
			t.Errorf("stripANSI(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeLogMsg(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"plain text", "plain text"},
		{"", ""},
		// ANSI escape sequences are stripped.
		{"\x1b[31mred\x1b[0m text", "red text"},
		{"\x1b[1;32mbold green\x1b[0m", "bold green"},
		// Tabs are replaced with 4 spaces.
		{"col1\tcol2\tcol3", "col1    col2    col3"},
		// Carriage returns are removed.
		{"progress\r100%", "progress100%"},
		// Mixed control characters.
		{"hello\r\x1b[Kworld", "helloworld"},
		// Backspace and other control chars dropped.
		{"abc\bdef", "abcdef"},
		// Newlines are preserved.
		{"line1\nline2", "line1\nline2"},
		// Combined: ANSI + tabs + CR.
		{"\x1b[33mwarn\x1b[0m:\tcheck\rfailed", "warn:    checkfailed"},
	}
	for _, tt := range tests {
		got := sanitizeLogMsg(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeLogMsg(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGraph(t *testing.T) {
	theme := DefaultTheme()

	t.Run("basic", func(t *testing.T) {
		data := []float64{0, 25, 50, 75, 100, 75, 50, 25}
		got := Graph(data, 4, 3, 100, &theme)
		lines := strings.Split(stripANSI(got), "\n")
		if len(lines) != 3 {
			t.Fatalf("expected 3 rows, got %d", len(lines))
		}
		for i, line := range lines {
			runes := []rune(line)
			if len(runes) != 4 {
				t.Errorf("row %d: expected 4 chars, got %d", i, len(runes))
			}
			for j, r := range runes {
				if r < 0x2800 || r > 0x28FF {
					t.Errorf("row %d char %d not braille: %U", i, j, r)
				}
			}
		}
	})

	t.Run("empty data", func(t *testing.T) {
		got := Graph(nil, 5, 2, 100, &theme)
		if got != "" {
			t.Errorf("expected empty for nil data, got %q", got)
		}
	})

	t.Run("auto scale", func(t *testing.T) {
		data := []float64{10, 20, 30}
		got := Graph(data, 2, 1, 0, &theme)
		if got == "" {
			t.Error("expected non-empty graph with auto-scale")
		}
	})

	t.Run("single row", func(t *testing.T) {
		data := []float64{50, 50}
		got := Graph(data, 1, 1, 100, &theme)
		lines := strings.Split(stripANSI(got), "\n")
		if len(lines) != 1 {
			t.Fatalf("expected 1 row, got %d", len(lines))
		}
	})

	t.Run("low value visible", func(t *testing.T) {
		// 0.5% CPU with maxVal=100 should still produce a visible braille char (not blank).
		data := []float64{0.5, 0.5}
		got := Graph(data, 1, 2, 100, &theme)
		plain := stripANSI(got)
		lines := strings.Split(plain, "\n")
		// Bottom row should have at least one non-blank braille character.
		lastLine := lines[len(lines)-1]
		runes := []rune(lastLine)
		hasVisible := false
		for _, r := range runes {
			if r != 0x2800 {
				hasVisible = true
			}
		}
		if !hasVisible {
			t.Error("low value should produce visible braille, got all blank")
		}
	})

	t.Run("exact codepoints 1row", func(t *testing.T) {
		// 2 data points: [100, 50], width=1, rows=1, maxVal=100.
		// totalDots=4. heights=[4, 2].
		// bottomDot=0, row=0 (only row).
		// Left: h=4, all 4 dots: 0x40|0x04|0x02|0x01 = 0x47
		// Right: h=2, bottom 2 dots: 0x80|0x20 = 0xA0
		// Pattern: 0x47|0xA0 = 0xE7 → U+28E7
		data := []float64{100, 50}
		got := Graph(data, 1, 1, 100, &theme)
		runes := []rune(stripANSI(got))
		if len(runes) != 1 {
			t.Fatalf("expected 1 char, got %d", len(runes))
		}
		expected := rune(0x2800 + 0xE7)
		if runes[0] != expected {
			t.Errorf("expected %U, got %U", expected, runes[0])
		}
	})
}

func TestHealthIndicator(t *testing.T) {
	theme := DefaultTheme()
	tests := []struct {
		health string
		symbol string
	}{
		{"healthy", "✓"},
		{"unhealthy", "✗"},
		{"starting", "!"},
		{"none", "–"},
		{"", "–"},
	}
	for _, tt := range tests {
		got := stripANSI(theme.HealthIndicator(tt.health))
		if got != tt.symbol {
			t.Errorf("HealthIndicator(%q) = %q, want %q", tt.health, got, tt.symbol)
		}
	}
}

func TestRestartColor(t *testing.T) {
	theme := DefaultTheme()
	if theme.RestartColor(0) != theme.Muted {
		t.Error("0 restarts should be Muted")
	}
	if theme.RestartColor(1) != theme.Warning {
		t.Error("1 restart should be Warning")
	}
	if theme.RestartColor(3) != theme.Critical {
		t.Error("3+ restarts should be Critical")
	}
}

func TestTimeMarkers(t *testing.T) {
	// Live mode returns nil.
	if got := timeMarkers(0); got != nil {
		t.Errorf("Live should return nil, got %v", got)
	}
	// Unknown window returns nil.
	if got := timeMarkers(999); got != nil {
		t.Errorf("unknown window should return nil, got %v", got)
	}

	tests := []struct {
		seconds int64
		count   int
		labels  []string
	}{
		{3600, 2, []string{"-20m", "-40m"}},
		{6 * 3600, 2, []string{"-2h", "-4h"}},
		{12 * 3600, 3, []string{"-3h", "-6h", "-9h"}},
		{24 * 3600, 3, []string{"-6h", "-12h", "-18h"}},
		{3 * 86400, 2, []string{"-1d", "-2d"}},
		{7 * 86400, 3, []string{"-2d", "-4d", "-6d"}},
	}
	for _, tt := range tests {
		vl := timeMarkers(tt.seconds)
		if len(vl) != tt.count {
			t.Errorf("timeMarkers(%d): got %d markers, want %d", tt.seconds, len(vl), tt.count)
			continue
		}
		for i, label := range tt.labels {
			if vl[i].Label != label {
				t.Errorf("timeMarkers(%d)[%d].Label = %q, want %q", tt.seconds, i, vl[i].Label, label)
			}
			if vl[i].Frac <= 0 || vl[i].Frac >= 1 {
				t.Errorf("timeMarkers(%d)[%d].Frac = %f, want 0 < frac < 1", tt.seconds, i, vl[i].Frac)
			}
		}
	}
}

func TestGraphWithGridVLines(t *testing.T) {
	theme := DefaultTheme()
	data := make([]float64, 40)
	for i := range data {
		data[i] = 50
	}

	t.Run("label appears", func(t *testing.T) {
		vlines := []VLine{{Frac: 0.5, Label: "-3h"}}
		got := GraphWithGrid(data, 20, 5, 100, []float64{50}, vlines, &theme)
		plain := stripANSI(got)
		if !strings.Contains(plain, "-3h") {
			t.Errorf("expected label '-3h' in output, got:\n%s", plain)
		}
	})

	t.Run("no vlines no label", func(t *testing.T) {
		got := GraphWithGrid(data, 20, 5, 100, []float64{50}, nil, &theme)
		plain := stripANSI(got)
		if strings.Contains(plain, "-3h") {
			t.Error("should not contain '-3h' without vlines")
		}
	})

	t.Run("multiple labels", func(t *testing.T) {
		vlines := []VLine{
			{Frac: 1.0 / 3.0, Label: "-2h"},
			{Frac: 2.0 / 3.0, Label: "-4h"},
		}
		got := GraphWithGrid(data, 30, 5, 100, []float64{50}, vlines, &theme)
		plain := stripANSI(got)
		if !strings.Contains(plain, "-2h") {
			t.Error("expected label '-2h'")
		}
		if !strings.Contains(plain, "-4h") {
			t.Error("expected label '-4h'")
		}
	})

	t.Run("row count preserved", func(t *testing.T) {
		vlines := []VLine{{Frac: 0.5, Label: "-1d"}}
		got := GraphWithGrid(data, 20, 4, 100, []float64{50}, vlines, &theme)
		lines := strings.Split(got, "\n")
		if len(lines) != 4 {
			t.Errorf("expected 4 rows, got %d", len(lines))
		}
	})

	t.Run("vline uses box drawing", func(t *testing.T) {
		vlines := []VLine{{Frac: 0.5, Label: "-3h"}}
		got := GraphWithGrid(data, 20, 10, 100, []float64{0, 100}, vlines, &theme)
		plain := stripANSI(got)
		if !strings.Contains(plain, "│") {
			t.Errorf("expected vline character │ in output, got:\n%s", plain)
		}
	})

	t.Run("grid row uses line drawing", func(t *testing.T) {
		// Use all-zero data so grid lines aren't obscured by data.
		zeroData := make([]float64, 40)
		got := GraphWithGrid(zeroData, 20, 6, 100, []float64{0, 50, 100}, nil, &theme)
		plain := stripANSI(got)
		if !strings.Contains(plain, "─") {
			t.Errorf("expected grid line character ─ in output, got:\n%s", plain)
		}
	})
}

func TestAutoGridGraphBreathingRoom(t *testing.T) {
	theme := DefaultTheme()

	t.Run("bumps ceiling when near max", func(t *testing.T) {
		// 72.0 → niceMaxBytes → 75M (if in bytes scale).
		// Using niceMax directly: 72.0 → 75. 72 > 75*0.9=67.5 → bump → niceMax(76) → 100.
		data := []float64{72.0}
		maxObs := 72.0
		maxVal := niceMax(maxObs)
		if maxVal != 75 {
			t.Fatalf("niceMax(72) = %v, want 75", maxVal)
		}
		// After breathing room: 72 > 75*0.9 = 67.5 → bump.
		bumped := niceMax(maxVal + 1)
		if bumped != 100 {
			t.Fatalf("niceMax(76) = %v, want 100", bumped)
		}

		// Verify via autoGridGraph: the ceiling label should show "100".
		lines := autoGridGraph(data, "72", 40, 6, 0, &theme, theme.Accent, graphAxis{
			ceilFn:  niceMax,
			labelFn: formatAutoLabel,
		})
		if len(lines) == 0 {
			t.Fatal("expected graph lines")
		}
		// First line should have the ceiling label.
		firstPlain := stripANSI(lines[0])
		if !strings.Contains(firstPlain, "100") {
			t.Errorf("ceiling should be bumped to 100, got: %q", firstPlain)
		}
	})

	t.Run("no bump when well below max", func(t *testing.T) {
		// 40 → niceMax → 50. 40 <= 50*0.9=45 → no bump.
		data := []float64{40.0}
		lines := autoGridGraph(data, "40", 40, 6, 0, &theme, theme.Accent, graphAxis{
			ceilFn:  niceMax,
			labelFn: formatAutoLabel,
		})
		if len(lines) == 0 {
			t.Fatal("expected graph lines")
		}
		firstPlain := stripANSI(lines[0])
		if !strings.Contains(firstPlain, "50") {
			t.Errorf("ceiling should be 50, got: %q", firstPlain)
		}
	})
}

func TestFormatContainerUptime(t *testing.T) {
	tests := []struct {
		state     string
		startedAt int64
		exitCode  int
		contains  string
	}{
		{"running", 0, 0, "—"},
		{"exited", 0, 137, "exit(137)"},
		{"exited", 0, 0, "exited"},
	}
	for _, tt := range tests {
		got := formatContainerUptime(tt.state, tt.startedAt, tt.exitCode)
		if !strings.Contains(got, tt.contains) {
			t.Errorf("formatContainerUptime(%q, %d, %d) = %q, want contains %q",
				tt.state, tt.startedAt, tt.exitCode, got, tt.contains)
		}
	}
}

func TestFormatContainerUptimeRunning(t *testing.T) {
	// Override nowFn for deterministic test.
	orig := nowFn
	defer func() { nowFn = orig }()

	fakeNow := time.Unix(1700100000, 0) // 100000 seconds after startedAt
	nowFn = func() time.Time { return fakeNow }

	got := formatContainerUptime("running", 1700000000, 0)
	// 100000 seconds = 1 day + 3 hours + ...
	if !strings.Contains(got, "up 1d") {
		t.Errorf("expected 'up 1d', got %q", got)
	}

	// 3600 seconds = 1h.
	nowFn = func() time.Time { return time.Unix(1700003600, 0) }
	got = formatContainerUptime("running", 1700000000, 0)
	if !strings.Contains(got, "up 1h") {
		t.Errorf("expected 'up 1h', got %q", got)
	}

	// 300 seconds = 5m.
	nowFn = func() time.Time { return time.Unix(1700000300, 0) }
	got = formatContainerUptime("running", 1700000000, 0)
	if !strings.Contains(got, "up 5m") {
		t.Errorf("expected 'up 5m', got %q", got)
	}
}

func TestFormatRestarts(t *testing.T) {
	theme := DefaultTheme()
	got := stripANSI(formatRestarts(5, &theme))
	if got != "5↻" {
		t.Errorf("formatRestarts(5) = %q, want 5↻", got)
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

func TestWrapTextBasic(t *testing.T) {
	lines := wrapText("hello world!", 5)
	want := []string{"hello", " worl", "d!"}
	if len(lines) != len(want) {
		t.Fatalf("wrapText lines = %d, want %d", len(lines), len(want))
	}
	for i, l := range lines {
		if l != want[i] {
			t.Errorf("wrapText[%d] = %q, want %q", i, l, want[i])
		}
	}
}

func TestWrapTextUnicode(t *testing.T) {
	lines := wrapText("日本語test", 3)
	if len(lines) == 0 {
		t.Fatal("expected wrapped lines")
	}
	runes := []rune(lines[0])
	if len(runes) != 3 {
		t.Errorf("first line has %d runes, want 3", len(runes))
	}
}

func TestWrapTextZeroWidth(t *testing.T) {
	lines := wrapText("hello", 0)
	if lines != nil {
		t.Errorf("zero width should return nil, got %v", lines)
	}
}

func TestWrapTextFits(t *testing.T) {
	lines := wrapText("hi", 10)
	if len(lines) != 1 || lines[0] != "hi" {
		t.Errorf("short text should fit in one line, got %v", lines)
	}
}

func TestWrapTextNewlines(t *testing.T) {
	// Newlines should produce separate lines, each wrapped independently.
	lines := wrapText("aaa\nbbb\nccc", 10)
	want := []string{"aaa", "bbb", "ccc"}
	if len(lines) != len(want) {
		t.Fatalf("wrapText newlines = %v, want %v", lines, want)
	}
	for i, l := range lines {
		if l != want[i] {
			t.Errorf("wrapText[%d] = %q, want %q", i, l, want[i])
		}
	}

	// Long lines between newlines should still wrap.
	lines = wrapText("abcdef\ngh", 4)
	want = []string{"abcd", "ef", "gh"}
	if len(lines) != len(want) {
		t.Fatalf("wrapText long+newline = %v, want %v", lines, want)
	}
	for i, l := range lines {
		if l != want[i] {
			t.Errorf("wrapText[%d] = %q, want %q", i, l, want[i])
		}
	}

	// Empty lines are preserved.
	lines = wrapText("a\n\nb", 10)
	want = []string{"a", "", "b"}
	if len(lines) != len(want) {
		t.Fatalf("wrapText empty line = %v, want %v", lines, want)
	}
	for i, l := range lines {
		if l != want[i] {
			t.Errorf("wrapText[%d] = %q, want %q", i, l, want[i])
		}
	}
}
