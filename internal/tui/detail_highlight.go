package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// highlightMatches renders text with regex matches highlighted using Reverse.
func highlightMatches(text string, re *regexp.Regexp, theme *Theme) string {
	matches := re.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return lipgloss.NewStyle().Foreground(theme.FgBright).Render(text)
	}

	normal := lipgloss.NewStyle().Foreground(theme.FgBright)
	highlight := lipgloss.NewStyle().Foreground(theme.FgBright).Reverse(true)

	var b strings.Builder
	prev := 0
	for _, m := range matches {
		if m[0] > prev {
			b.WriteString(normal.Render(text[prev:m[0]]))
		}
		b.WriteString(highlight.Render(text[m[0]:m[1]]))
		prev = m[1]
	}
	if prev < len(text) {
		b.WriteString(normal.Render(text[prev:]))
	}
	return b.String()
}

// rawToPrettyMap builds a byte-position mapping from raw JSON to pretty-printed JSON.
// Each non-whitespace byte outside strings maps 1:1; added whitespace in pretty is skipped.
func rawToPrettyMap(raw, pretty string) []int {
	m := make([]int, len(raw))
	j, inStr, esc := 0, false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !inStr && (c == ' ' || c == '\t' || c == '\n' || c == '\r') {
			m[i] = j
			continue
		}
		if !inStr {
			for j < len(pretty) && (pretty[j] == ' ' || pretty[j] == '\t' || pretty[j] == '\n' || pretty[j] == '\r') {
				j++
			}
		}
		m[i] = j
		j++
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		}
	}
	return m
}

// highlightRanges renders text with pre-computed byte ranges highlighted.
// offset is the byte position of line within the full text.
func highlightRanges(line string, offset int, ranges [][]int, theme *Theme) string {
	normal := lipgloss.NewStyle().Foreground(theme.FgBright)
	highlight := lipgloss.NewStyle().Foreground(theme.FgBright).Reverse(true)
	var b strings.Builder
	prev := 0
	for _, r := range ranges {
		s, e := r[0]-offset, r[1]-offset
		if s < 0 {
			s = 0
		}
		if e > len(line) {
			e = len(line)
		}
		if s >= e {
			continue
		}
		if s > prev {
			b.WriteString(normal.Render(line[prev:s]))
		}
		b.WriteString(highlight.Render(line[s:e]))
		prev = e
	}
	if prev == 0 {
		return normal.Render(line)
	}
	if prev < len(line) {
		b.WriteString(normal.Render(line[prev:]))
	}
	return b.String()
}
