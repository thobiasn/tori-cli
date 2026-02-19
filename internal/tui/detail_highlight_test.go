package tui

import (
	"regexp"
	"strings"
	"testing"
)

func TestHighlightMatchesNoMatch(t *testing.T) {
	theme := TerminalTheme()
	re := regexp.MustCompile("(?i)xyz")
	result := highlightMatches("hello world", re, &theme)

	if !strings.Contains(result, "hello world") {
		t.Errorf("expected original text in output, got %q", result)
	}
}

func TestHighlightMatchesSingleMatch(t *testing.T) {
	theme := TerminalTheme()
	re := regexp.MustCompile("(?i)world")
	result := highlightMatches("hello world", re, &theme)

	if !strings.Contains(result, "hello") {
		t.Error("expected 'hello' in output")
	}
	if !strings.Contains(result, "world") {
		t.Error("expected 'world' in output")
	}
}

func TestHighlightMatchesMultipleMatches(t *testing.T) {
	theme := TerminalTheme()
	re := regexp.MustCompile("o")
	result := highlightMatches("foo bar boo", re, &theme)

	// All text should be present.
	if !strings.Contains(result, "f") {
		t.Error("expected 'f' in output")
	}
	if !strings.Contains(result, "bar") {
		t.Error("expected 'bar' in output")
	}
	if !strings.Contains(result, "b") {
		t.Error("expected 'b' in output")
	}
}

func TestHighlightMatchesFullMatch(t *testing.T) {
	theme := TerminalTheme()
	re := regexp.MustCompile(".*")
	result := highlightMatches("entire text", re, &theme)
	if !strings.Contains(result, "entire text") {
		t.Errorf("expected full text in output, got %q", result)
	}
}

func TestHighlightMatchesEmptyText(t *testing.T) {
	theme := TerminalTheme()
	re := regexp.MustCompile("(?i)test")
	// Should not panic.
	_ = highlightMatches("", re, &theme)
}

func TestRawToPrettyMapSimple(t *testing.T) {
	raw := `{"a":1,"b":2}`
	pretty := "{\n  \"a\": 1,\n  \"b\": 2\n}"

	m := rawToPrettyMap(raw, pretty)

	if len(m) != len(raw) {
		t.Fatalf("map length = %d, want %d", len(m), len(raw))
	}

	// The opening brace in raw should map to the opening brace in pretty.
	if m[0] != 0 {
		t.Errorf("m[0] = %d, want 0 (both start with '{')", m[0])
	}

	// The closing brace in raw should map near the end of pretty.
	lastRaw := len(raw) - 1
	lastPretty := len(pretty) - 1
	if m[lastRaw] != lastPretty {
		t.Errorf("m[%d] = %d, want %d (closing braces should align)", lastRaw, m[lastRaw], lastPretty)
	}
}

func TestRawToPrettyMapEmpty(t *testing.T) {
	m := rawToPrettyMap("", "")
	if len(m) != 0 {
		t.Errorf("expected empty map for empty input, got length %d", len(m))
	}
}

func TestRawToPrettyMapStringValues(t *testing.T) {
	raw := `{"key":"value"}`
	pretty := "{\n  \"key\": \"value\"\n}"

	m := rawToPrettyMap(raw, pretty)

	// Find 'v' in "value" in raw.
	vIdx := strings.Index(raw, "value")
	if vIdx < 0 {
		t.Fatal("'value' not found in raw")
	}

	// The mapped position should point to 'v' in "value" in pretty.
	prettyVIdx := strings.Index(pretty, "value")
	if m[vIdx] != prettyVIdx {
		t.Errorf("'v' in raw maps to %d, want %d", m[vIdx], prettyVIdx)
	}
}

func TestRawToPrettyMapEscapedString(t *testing.T) {
	raw := `{"k":"a\"b"}`
	pretty := "{\n  \"k\": \"a\\\"b\"\n}"

	m := rawToPrettyMap(raw, pretty)

	// Should not panic and should produce a valid mapping.
	if len(m) != len(raw) {
		t.Fatalf("map length = %d, want %d", len(m), len(raw))
	}
}

func TestRawToPrettyMapMonotonic(t *testing.T) {
	raw := `{"a":"x","b":"y"}`
	pretty := "{\n  \"a\": \"x\",\n  \"b\": \"y\"\n}"

	m := rawToPrettyMap(raw, pretty)

	// Positions should be monotonically non-decreasing.
	for i := 1; i < len(m); i++ {
		if m[i] < m[i-1] {
			t.Errorf("non-monotonic: m[%d]=%d < m[%d]=%d", i, m[i], i-1, m[i-1])
		}
	}
}

func TestHighlightRangesNoOverlap(t *testing.T) {
	theme := TerminalTheme()
	line := "hello world test"
	ranges := [][]int{{0, 5}, {12, 16}}

	result := highlightRanges(line, 0, ranges, &theme)

	if !strings.Contains(result, "hello") {
		t.Error("expected 'hello' in output")
	}
	if !strings.Contains(result, "world") {
		t.Error("expected 'world' in output")
	}
	if !strings.Contains(result, "test") {
		t.Error("expected 'test' in output")
	}
}

func TestHighlightRangesWithOffset(t *testing.T) {
	theme := TerminalTheme()
	line := "error here"
	ranges := [][]int{{10, 15}}

	result := highlightRanges(line, 10, ranges, &theme)

	// Should contain "error" and "here".
	if !strings.Contains(result, "error") {
		t.Error("expected 'error' in output")
	}
	if !strings.Contains(result, " here") {
		t.Error("expected ' here' in output")
	}
}

func TestHighlightRangesNoRanges(t *testing.T) {
	theme := TerminalTheme()
	line := "hello world"

	result := highlightRanges(line, 0, nil, &theme)

	if !strings.Contains(result, "hello world") {
		t.Errorf("expected original text, got %q", result)
	}
}

func TestHighlightRangesOutOfBounds(t *testing.T) {
	theme := TerminalTheme()
	line := "short"
	ranges := [][]int{{0, 100}}

	// Should not panic; should highlight as much as possible.
	result := highlightRanges(line, 0, ranges, &theme)

	if !strings.Contains(result, "short") {
		t.Errorf("expected 'short' in output, got %q", result)
	}
}

func TestHighlightRangesBeforeOffset(t *testing.T) {
	theme := TerminalTheme()
	line := "hello"
	ranges := [][]int{{0, 3}}

	// Range entirely before this line's offset — should render without crashing.
	result := highlightRanges(line, 10, ranges, &theme)

	if !strings.Contains(result, "hello") {
		t.Errorf("expected 'hello' in output, got %q", result)
	}
}

func TestHighlightRangesAdjacentRanges(t *testing.T) {
	theme := TerminalTheme()
	line := "abcdef"
	// Adjacent ranges: [0,3) and [3,6).
	ranges := [][]int{{0, 3}, {3, 6}}

	result := highlightRanges(line, 0, ranges, &theme)

	if !strings.Contains(result, "abc") {
		t.Error("expected 'abc' in output")
	}
	if !strings.Contains(result, "def") {
		t.Error("expected 'def' in output")
	}
}
