package tui

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Box renders a bordered panel with a title using rounded Unicode corners.
// Content is padded to fill width×height (including borders).
func Box(title, content string, width, height int, theme *Theme) string {
	if width < 4 {
		width = 4
	}
	if height < 3 {
		height = 3
	}

	innerW := width - 2 // subtract left+right border chars

	// Top border with embedded title.
	var top string
	if title != "" {
		titleStr := " " + title + " "
		titleLen := lipgloss.Width(titleStr)
		if titleLen > innerW-2 {
			titleStr = Truncate(titleStr, innerW-2)
			titleLen = lipgloss.Width(titleStr)
		}
		styled := lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render(titleStr)
		// Budget: "╭" + leading "─" + title + trailing "─"s + "╮"
		// The leading "─" costs 1 char, so trailing fill = innerW - 1 - titleLen
		trailing := innerW - 1 - titleLen
		if trailing < 0 {
			trailing = 0
		}
		top = "╭─" + styled + strings.Repeat("─", trailing) + "╮"
	} else {
		top = "╭" + strings.Repeat("─", innerW) + "╮"
	}

	// Content lines.
	lines := strings.Split(content, "\n")
	innerH := height - 2 // subtract top+bottom borders
	// Pad or truncate to fit inner height.
	for len(lines) < innerH {
		lines = append(lines, "")
	}
	if len(lines) > innerH {
		lines = lines[:innerH]
	}

	var b strings.Builder
	b.WriteString(top)
	b.WriteByte('\n')
	for _, line := range lines {
		lineW := lipgloss.Width(line)
		pad := innerW - lineW
		if pad < 0 {
			pad = 0
			line = Truncate(line, innerW)
		}
		b.WriteString("│")
		b.WriteString(line)
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString("│\n")
	}
	b.WriteString("╰")
	b.WriteString(strings.Repeat("─", innerW))
	b.WriteString("╯")

	return b.String()
}

// ProgressBar renders a horizontal bar like [████░░░░] 58.2%.
// Width is the total character width including brackets, space, and percentage.
func ProgressBar(percent float64, width int, theme *Theme) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	label := fmt.Sprintf(" %5.1f%%", percent)
	labelLen := len(label)
	barW := width - 2 - labelLen // subtract [] and label
	if barW < 1 {
		barW = 1
	}

	filled := int(float64(barW) * percent / 100)
	if filled > barW {
		filled = barW
	}

	color := theme.UsageColor(percent)
	style := lipgloss.NewStyle().Foreground(color)

	filledStr := style.Render(strings.Repeat("█", filled))
	emptyStr := lipgloss.NewStyle().Foreground(theme.Muted).Render(strings.Repeat("░", barW-filled))

	return "[" + filledStr + emptyStr + "]" + label
}

// Sparkline renders a single row of braille characters representing data points.
// Each braille character encodes two data points (left and right columns).
// Values are normalized to 0–4 dots high in the 2×4 braille grid.
func Sparkline(data []float64, width int, theme *Theme) string {
	if width < 1 || len(data) == 0 {
		return ""
	}

	// Each braille char covers 2 data points.
	maxPoints := width * 2
	if len(data) > maxPoints {
		data = data[len(data)-maxPoints:]
	}

	// Find max for normalization.
	maxVal := 0.0
	for _, v := range data {
		if v > maxVal {
			maxVal = v
		}
	}

	// Map each value to height 0–4 (4 rows per braille character).
	heights := make([]int, len(data))
	for i, v := range data {
		if maxVal > 0 {
			h := int(v / maxVal * 4)
			if h > 4 {
				h = 4
			}
			heights[i] = h
		}
	}

	// Braille dot bit layout (Unicode U+2800 base):
	//   Row 0: bit 0 (col 0), bit 3 (col 1)
	//   Row 1: bit 1 (col 0), bit 4 (col 1)
	//   Row 2: bit 2 (col 0), bit 5 (col 1)
	//   Row 3: bit 6 (col 0), bit 7 (col 1)
	// We fill from bottom (row 3) up.
	leftBits := [4]byte{0x40, 0x04, 0x02, 0x01}  // row3, row2, row1, row0
	rightBits := [4]byte{0x80, 0x20, 0x10, 0x08} // row3, row2, row1, row0

	chars := make([]rune, 0, width)
	for i := 0; i < len(heights); i += 2 {
		var pattern byte
		for row := 0; row < heights[i]; row++ {
			pattern |= leftBits[row]
		}
		if i+1 < len(heights) {
			for row := 0; row < heights[i+1]; row++ {
				pattern |= rightBits[row]
			}
		}
		chars = append(chars, rune(0x2800+int(pattern)))
	}

	// Pad to width if fewer data points than capacity.
	for len(chars) < width {
		chars = append(chars, 0x2800)
	}

	// Color based on the last value's usage level.
	lastPercent := data[len(data)-1]
	color := theme.UsageColor(lastPercent)
	return lipgloss.NewStyle().Foreground(color).Render(string(chars))
}

// FormatBytes formats a byte count for human display.
func FormatBytes(bytes uint64) string {
	switch {
	case bytes >= 1<<40:
		return fmt.Sprintf("%.1fT", float64(bytes)/float64(uint64(1)<<40))
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(bytes)/float64(uint64(1)<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(bytes)/float64(uint64(1)<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(bytes)/float64(uint64(1)<<10))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

// FormatBytesRate formats bytes/second for display.
func FormatBytesRate(bytesPerSec float64) string {
	switch {
	case bytesPerSec >= 1e9:
		return fmt.Sprintf("%.1fGB/s", bytesPerSec/1e9)
	case bytesPerSec >= 1e6:
		return fmt.Sprintf("%.1fMB/s", bytesPerSec/1e6)
	case bytesPerSec >= 1e3:
		return fmt.Sprintf("%.1fKB/s", bytesPerSec/1e3)
	default:
		return fmt.Sprintf("%.0fB/s", bytesPerSec)
	}
}

// FormatUptime formats seconds into a human-readable duration.
func FormatUptime(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

// FormatTimestamp formats a Unix timestamp as HH:MM:SS.
func FormatTimestamp(ts int64) string {
	t := time.Unix(ts, 0)
	return t.Format("15:04:05")
}

// Truncate shortens a plain (non-styled) string to maxLen, appending … if truncated.
func Truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen == 1 {
		return "…"
	}
	return string(runes[:maxLen-1]) + "…"
}

// TruncateStyled shortens a string that may contain ANSI escape sequences.
// If the visual width fits, the string is returned as-is. Otherwise, ANSI
// is stripped and the plain text is truncated.
func TruncateStyled(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxLen {
		return s
	}
	return Truncate(stripANSI(s), maxLen)
}

// stripANSI removes ANSI escape sequences from a string.
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

// ContainerNameColor returns a deterministic color for a container name
// using FNV-32a hash into the theme's container palette.
func ContainerNameColor(name string, theme *Theme) lipgloss.Color {
	h := fnv.New32a()
	h.Write([]byte(name))
	palette := theme.ContainerPalette
	return palette[h.Sum32()%uint32(len(palette))]
}
