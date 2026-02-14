package tui

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

// RenderContext carries shared rendering state passed through panel functions.
type RenderContext struct {
	Width        int
	Height       int
	Theme        *Theme
	WindowLabel  string
	WindowSec    int64
	SpinnerFrame int
}

// Overlay composites fg centered on top of bg. Both strings are
// newline-separated terminal renderings. Width and height are the total
// terminal dimensions. Uses ANSI-aware string operations so styled
// background content is preserved around the overlay edges.
func Overlay(bg, fg string, width, height int) string {
	bgLines := strings.Split(bg, "\n")
	fgLines := strings.Split(fg, "\n")

	fgH := len(fgLines)
	fgW := 0
	for _, l := range fgLines {
		if w := lipgloss.Width(l); w > fgW {
			fgW = w
		}
	}

	x := (width - fgW) / 2
	y := (height - fgH) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	// Ensure bg has enough lines.
	for len(bgLines) < height {
		bgLines = append(bgLines, "")
	}

	for i, fgLine := range fgLines {
		row := y + i
		if row >= len(bgLines) {
			break
		}
		bgLine := bgLines[row]
		fgLineW := lipgloss.Width(fgLine)

		left := ansi.Truncate(bgLine, x, "")
		if leftW := lipgloss.Width(left); leftW < x {
			left += strings.Repeat(" ", x-leftW)
		}

		right := ansi.TruncateLeft(bgLine, x+fgLineW, "")
		bgLines[row] = left + fgLine + right
	}

	if len(bgLines) > height {
		bgLines = bgLines[:height]
	}
	return strings.Join(bgLines, "\n")
}

// Box renders a bordered panel with a title using rounded Unicode corners.
// Content is padded to fill width×height (including borders).
// An optional focused parameter controls focusable panel styling:
//   - true: border and title in theme.Accent (cyan)
//   - false: border and title in theme.Muted (gray)
//   - omitted: default (title accent+bold, borders uncolored)
func Box(title, content string, width, height int, theme *Theme, focused ...bool) string {
	if width < 4 {
		width = 4
	}
	if height < 3 {
		height = 3
	}

	innerW := width - 2 // subtract left+right border chars

	// Determine border/title styling based on focus state.
	var borderStyle lipgloss.Style
	var titleStyle lipgloss.Style
	if len(focused) > 0 {
		if focused[0] {
			borderStyle = lipgloss.NewStyle().Foreground(theme.Accent)
			titleStyle = lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)
		} else {
			borderStyle = lipgloss.NewStyle().Foreground(theme.Muted)
			titleStyle = lipgloss.NewStyle().Foreground(theme.Muted)
		}
	} else {
		titleStyle = lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)
	}

	// Top border with embedded title.
	var top string
	if title != "" {
		titleStr := " " + title + " "
		titleLen := lipgloss.Width(titleStr)
		if titleLen > innerW-2 {
			titleStr = Truncate(titleStr, innerW-2)
			titleLen = lipgloss.Width(titleStr)
		}
		styled := titleStyle.Render(titleStr)
		trailing := innerW - 1 - titleLen
		if trailing < 0 {
			trailing = 0
		}
		top = borderStyle.Render("╭─") + styled + borderStyle.Render(strings.Repeat("─", trailing)+"╮")
	} else {
		top = borderStyle.Render("╭" + strings.Repeat("─", innerW) + "╮")
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
			line = TruncateStyled(line, innerW)
		}
		b.WriteString(borderStyle.Render("│"))
		b.WriteString(line)
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString(borderStyle.Render("│"))
		b.WriteByte('\n')
	}
	b.WriteString(borderStyle.Render("╰" + strings.Repeat("─", innerW) + "╯"))

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

// ProgressBarSimple renders a horizontal bar like [████░░░░] without percentage label.
// Width is the total character width including brackets.
func ProgressBarSimple(percent float64, width int, theme *Theme) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	barW := width - 2 // subtract []
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

	return "[" + filledStr + emptyStr + "]"
}

// ProgressBarFixedColor renders a horizontal bar like [████░░░░] in a single fixed color.
func ProgressBarFixedColor(percent float64, width int, color lipgloss.Color, theme *Theme) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	barW := width - 2 // subtract []
	if barW < 1 {
		barW = 1
	}

	filled := int(float64(barW) * percent / 100)
	if filled > barW {
		filled = barW
	}

	style := lipgloss.NewStyle().Foreground(color)
	filledStr := style.Render(strings.Repeat("█", filled))
	emptyStr := lipgloss.NewStyle().Foreground(theme.Muted).Render(strings.Repeat("░", barW-filled))

	return "[" + filledStr + emptyStr + "]"
}

// Sparkline renders a single row of braille characters representing data points.
// Each braille character encodes two data points (left and right columns).
// Values are normalized to 0–4 dots high in the 2×4 braille grid.
func Sparkline(data []float64, width int, theme *Theme) string {
	if width < 1 || len(data) == 0 {
		return ""
	}

	// Each braille char covers 2 data points.
	data = fitToWidth(data, width*2)

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
			if h < 1 && v > 0 {
				h = 1
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

// fitToWidth compresses data to fit within maxPoints by taking the max
// value in each group of consecutive points. This preserves peaks and
// maintains time alignment (unlike truncation which discards old data).
func fitToWidth(data []float64, maxPoints int) []float64 {
	if len(data) <= maxPoints {
		return data
	}
	out := make([]float64, maxPoints)
	ratio := float64(len(data)) / float64(maxPoints)
	for i := range out {
		lo := int(float64(i) * ratio)
		hi := int(float64(i+1) * ratio)
		if hi > len(data) {
			hi = len(data)
		}
		var mx float64
		for j := lo; j < hi; j++ {
			if data[j] > mx {
				mx = data[j]
			}
		}
		out[i] = mx
	}
	return out
}

// Graph renders a multi-row braille graph. Each braille character covers
// 2 data points horizontally and 4 dot rows vertically. With `rows` rows of
// braille characters the vertical resolution is rows*4 dots.
// If maxVal <= 0, auto-scales to the data's maximum.
func Graph(data []float64, width, rows int, maxVal float64, theme *Theme) string {
	if width < 1 || rows < 1 || len(data) == 0 {
		return ""
	}

	data = fitToWidth(data, width*2)

	if maxVal <= 0 {
		for _, v := range data {
			if v > maxVal {
				maxVal = v
			}
		}
	}
	if maxVal <= 0 {
		maxVal = 1
	}

	totalDots := rows * 4

	heights := make([]int, len(data))
	for i, v := range data {
		h := int(v / maxVal * float64(totalDots))
		if h > totalDots {
			h = totalDots
		}
		if h < 0 {
			h = 0
		}
		if h < 1 && v > 0 {
			h = 1
		}
		heights[i] = h
	}

	leftBits := [4]byte{0x40, 0x04, 0x02, 0x01}
	rightBits := [4]byte{0x80, 0x20, 0x10, 0x08}

	// Build row by row, top to bottom.
	rowStrs := make([]string, rows)
	for r := 0; r < rows; r++ {
		bottomDot := (rows - 1 - r) * 4

		var chars []rune
		for col := 0; col < len(heights); col += 2 {
			var pattern byte
			lh := heights[col]
			for dot := 0; dot < 4; dot++ {
				if lh > bottomDot+dot {
					pattern |= leftBits[dot]
				}
			}
			if col+1 < len(heights) {
				rh := heights[col+1]
				for dot := 0; dot < 4; dot++ {
					if rh > bottomDot+dot {
						pattern |= rightBits[dot]
					}
				}
			}
			chars = append(chars, rune(0x2800+int(pattern)))
		}

		// Pad to width (left-pad with empty braille).
		if pad := width - len(chars); pad > 0 {
			padded := make([]rune, width)
			for p := 0; p < pad; p++ {
				padded[p] = 0x2800
			}
			copy(padded[pad:], chars)
			chars = padded
		}

		// Color based on the vertical position this row represents.
		rowTopPct := float64(bottomDot+4) / float64(totalDots) * 100
		color := theme.UsageColor(rowTopPct)
		rowStrs[r] = lipgloss.NewStyle().Foreground(color).Render(string(chars))
	}

	return strings.Join(rowStrs, "\n")
}

// VLine describes a vertical marker at a fractional position across a graph.
type VLine struct {
	Frac  float64 // 0.0 = right edge (now), 1.0 = left edge (oldest)
	Label string  // e.g. "-2h"
}

// timeMarkers returns vertical line markers for a given time window.
// Returns nil for Live mode (seconds == 0) or unknown windows.
func timeMarkers(seconds int64) []VLine {
	type entry struct {
		offset int64
		label  string
	}
	var markers []entry
	switch seconds {
	case 3600: // 1h → 20min intervals
		markers = []entry{{1200, "-20m"}, {2400, "-40m"}}
	case 6 * 3600: // 6h → 2h intervals
		markers = []entry{{2 * 3600, "-2h"}, {4 * 3600, "-4h"}}
	case 12 * 3600: // 12h → 3h intervals
		markers = []entry{{3 * 3600, "-3h"}, {6 * 3600, "-6h"}, {9 * 3600, "-9h"}}
	case 24 * 3600: // 24h → 6h intervals
		markers = []entry{{6 * 3600, "-6h"}, {12 * 3600, "-12h"}, {18 * 3600, "-18h"}}
	case 3 * 86400: // 3d → 1d intervals
		markers = []entry{{86400, "-1d"}, {2 * 86400, "-2d"}}
	case 7 * 86400: // 7d → 2d intervals
		markers = []entry{{2 * 86400, "-2d"}, {4 * 86400, "-4d"}, {6 * 86400, "-6d"}}
	default:
		return nil
	}
	vlines := make([]VLine, len(markers))
	for i, m := range markers {
		vlines[i] = VLine{
			Frac:  float64(m.offset) / float64(seconds),
			Label: m.label,
		}
	}
	return vlines
}

// GraphWithGrid renders a multi-row braille graph with horizontal grid lines
// and optional vertical time markers. gridPcts are percentage values (0-100)
// at which dashed horizontal lines are drawn. vlines adds vertical dashed lines
// with centered labels (pass nil to skip). maxVal must be >0.
func GraphWithGrid(data []float64, width, rows int, maxVal float64, gridPcts []float64, vlines []VLine, theme *Theme, fixedColor ...lipgloss.Color) string {
	if width < 1 || rows < 1 || maxVal <= 0 {
		return ""
	}

	data = fitToWidth(data, width*2)

	totalDots := rows * 4

	// Normalize data heights.
	heights := make([]int, len(data))
	for i, v := range data {
		h := int(v / maxVal * float64(totalDots))
		if h > totalDots {
			h = totalDots
		}
		if h < 0 {
			h = 0
		}
		if h < 1 && v > 0 {
			h = 1
		}
		heights[i] = h
	}

	// Convert grid percentages to character rows.
	gridDots := make(map[int]bool, len(gridPcts))
	for _, pct := range gridPcts {
		dot := int(pct / 100 * float64(totalDots))
		if dot < 0 {
			dot = 0
		}
		if dot >= totalDots {
			dot = totalDots - 1
		}
		gridDots[dot] = true
	}
	gridRows := make(map[int]bool)
	for dot := range gridDots {
		r := rows - 1 - dot/4
		gridRows[r] = true
	}

	leftBits := [4]byte{0x40, 0x04, 0x02, 0x01}
	rightBits := [4]byte{0x80, 0x20, 0x10, 0x08}

	// Pre-compute vertical line columns and label positions.
	type vlineCol struct {
		col        int
		label      []rune
		labelStart int // -1 if label doesn't fit
	}
	var vcols []vlineCol
	labelRow := rows / 2
	for _, vl := range vlines {
		col := int((1.0 - vl.Frac) * float64(width-1))
		if col < 1 || col >= width-1 {
			continue
		}
		lr := []rune(vl.Label)
		vc := vlineCol{col: col, label: lr, labelStart: -1}
		half := len(lr) / 2
		start := col - half
		end := start + len(lr)
		if start >= 1 && end <= width-1 {
			vc.labelStart = start
		}
		vcols = append(vcols, vc)
	}
	// Sort vcols by column (left to right) for overlap detection.
	for i := 1; i < len(vcols); i++ {
		for j := i; j > 0 && vcols[j-1].col > vcols[j].col; j-- {
			vcols[j-1], vcols[j] = vcols[j], vcols[j-1]
		}
	}
	// Remove overlapping labels.
	for i := 1; i < len(vcols); i++ {
		if vcols[i].labelStart < 0 {
			continue
		}
		prevEnd := -1
		for j := i - 1; j >= 0; j-- {
			if vcols[j].labelStart >= 0 {
				prevEnd = vcols[j].labelStart + len(vcols[j].label)
				break
			}
		}
		if prevEnd > 0 && vcols[i].labelStart < prevEnd+1 {
			vcols[i].labelStart = -1
		}
	}
	// Build vline column set for O(1) lookup and label char map.
	vcolSet := make(map[int]bool, len(vcols))
	for _, vc := range vcols {
		vcolSet[vc.col] = true
	}
	labelChars := make(map[int]rune, len(vcols)*4)
	for _, vc := range vcols {
		if vc.labelStart < 0 {
			continue
		}
		for j, ch := range vc.label {
			labelChars[vc.labelStart+j] = ch
		}
	}

	gridStyle := lipgloss.NewStyle().Foreground(theme.Grid)
	muted := lipgloss.NewStyle().Foreground(theme.Muted)

	type cellKind int
	const (
		kindEmpty cellKind = iota
		kindData
		kindGrid
	)

	rowStrs := make([]string, rows)
	for r := 0; r < rows; r++ {
		bottomDot := (rows - 1 - r) * 4
		isLabelRow := len(vcols) > 0 && r == labelRow
		isGridRow := gridRows[r]

		// Build data braille patterns.
		dataChars := make([]rune, width)
		hasData := make([]bool, width)
		for col := 0; col < len(heights); col += 2 {
			charIdx := width - (len(heights)-col+1)/2
			if charIdx < 0 {
				continue
			}
			var pattern byte
			lh := heights[col]
			for dot := 0; dot < 4; dot++ {
				if lh > bottomDot+dot {
					pattern |= leftBits[dot]
				}
			}
			if col+1 < len(heights) {
				rh := heights[col+1]
				for dot := 0; dot < 4; dot++ {
					if rh > bottomDot+dot {
						pattern |= rightBits[dot]
					}
				}
			}
			dataChars[charIdx] = rune(0x2800 + int(pattern))
			hasData[charIdx] = pattern != 0
		}

		// Compose cells: determine character and kind for each column.
		type cell struct {
			ch   rune
			kind cellKind
		}
		cells := make([]cell, width)
		for i := 0; i < width; i++ {
			isVCol := vcolSet[i]

			// Label row: label chars override everything at their positions.
			if isLabelRow {
				if lch, ok := labelChars[i]; ok {
					cells[i] = cell{lch, kindGrid}
					continue
				}
			}

			// Grid infrastructure (vlines and hlines).
			vDash := isVCol && r%3 == 0 // vertical: every 3rd row
			if vDash && isGridRow {
				cells[i] = cell{'┼', kindGrid}
				continue
			}
			if vDash && !isLabelRow {
				cells[i] = cell{'│', kindGrid}
				continue
			}
			if isGridRow && !isVCol {
				if hasData[i] {
					cells[i] = cell{dataChars[i], kindData}
				} else {
					cells[i] = cell{'─', kindGrid}
				}
				continue
			}

			// Normal cell: data or empty braille.
			if hasData[i] {
				cells[i] = cell{dataChars[i], kindData}
			} else {
				ch := dataChars[i]
				if ch == 0 {
					ch = 0x2800
				}
				cells[i] = cell{ch, kindEmpty}
			}
		}

		// Group consecutive same-kind cells into runs.
		var b strings.Builder
		type run struct {
			kind  cellKind
			chars []rune
		}
		var runs []run
		for _, c := range cells {
			if len(runs) > 0 && runs[len(runs)-1].kind == c.kind {
				runs[len(runs)-1].chars = append(runs[len(runs)-1].chars, c.ch)
			} else {
				runs = append(runs, run{c.kind, []rune{c.ch}})
			}
		}

		var dataColor lipgloss.Color
		if len(fixedColor) > 0 && fixedColor[0] != "" {
			dataColor = fixedColor[0]
		} else {
			rowTopPct := float64(bottomDot+4) / float64(totalDots) * maxVal
			dataColor = theme.UsageColor(rowTopPct)
		}
		dataStyle := lipgloss.NewStyle().Foreground(dataColor)

		for _, rn := range runs {
			s := string(rn.chars)
			switch rn.kind {
			case kindData:
				b.WriteString(dataStyle.Render(s))
			case kindGrid:
				b.WriteString(gridStyle.Render(s))
			default:
				b.WriteString(muted.Render(s))
			}
		}

		rowStrs[r] = b.String()
	}

	return strings.Join(rowStrs, "\n")
}

// GraphFixedColor renders a multi-row braille graph in a single fixed color.
// Same logic as Graph but uses the given color uniformly instead of per-row UsageColor.
func GraphFixedColor(data []float64, width, rows int, maxVal float64, color lipgloss.Color) string {
	if width < 1 || rows < 1 || len(data) == 0 {
		return ""
	}

	data = fitToWidth(data, width*2)

	if maxVal <= 0 {
		for _, v := range data {
			if v > maxVal {
				maxVal = v
			}
		}
	}
	if maxVal <= 0 {
		maxVal = 1
	}

	totalDots := rows * 4

	heights := make([]int, len(data))
	for i, v := range data {
		h := int(v / maxVal * float64(totalDots))
		if h > totalDots {
			h = totalDots
		}
		if h < 0 {
			h = 0
		}
		if h < 1 && v > 0 {
			h = 1
		}
		heights[i] = h
	}

	leftBits := [4]byte{0x40, 0x04, 0x02, 0x01}
	rightBits := [4]byte{0x80, 0x20, 0x10, 0x08}

	style := lipgloss.NewStyle().Foreground(color)
	rowStrs := make([]string, rows)
	for r := 0; r < rows; r++ {
		bottomDot := (rows - 1 - r) * 4

		var chars []rune
		for col := 0; col < len(heights); col += 2 {
			var pattern byte
			lh := heights[col]
			for dot := 0; dot < 4; dot++ {
				if lh > bottomDot+dot {
					pattern |= leftBits[dot]
				}
			}
			if col+1 < len(heights) {
				rh := heights[col+1]
				for dot := 0; dot < 4; dot++ {
					if rh > bottomDot+dot {
						pattern |= rightBits[dot]
					}
				}
			}
			chars = append(chars, rune(0x2800+int(pattern)))
		}

		if pad := width - len(chars); pad > 0 {
			padded := make([]rune, width)
			for p := 0; p < pad; p++ {
				padded[p] = 0x2800
			}
			copy(padded[pad:], chars)
			chars = padded
		}

		rowStrs[r] = style.Render(string(chars))
	}

	return strings.Join(rowStrs, "\n")
}

// nowFn is the time source for formatContainerUptime. Tests override for determinism.
var nowFn = time.Now

// formatContainerUptime returns a short uptime string for a container.
func formatContainerUptime(state string, startedAt int64, exitCode int) string {
	if state != "running" {
		if exitCode != 0 {
			return fmt.Sprintf("exit(%d)", exitCode)
		}
		return state
	}
	if startedAt <= 0 {
		return "—"
	}
	secs := nowFn().Unix() - startedAt
	if secs < 0 {
		secs = 0
	}
	d := time.Duration(secs) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("up %dd", days)
	}
	if hours > 0 {
		return fmt.Sprintf("up %dh", hours)
	}
	return fmt.Sprintf("up %dm", mins)
}

// formatRestarts returns a styled restart count string.
func formatRestarts(count int, theme *Theme) string {
	s := fmt.Sprintf("%d↻", count)
	color := theme.RestartColor(count)
	return lipgloss.NewStyle().Foreground(color).Render(s)
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

// FormatTimestamp formats a Unix timestamp using the given Go time layout.
func FormatTimestamp(ts int64, format string) string {
	return time.Unix(ts, 0).Format(format)
}

// FormatNumber formats an integer with comma separators (e.g., 2847 → "2,847").
func FormatNumber(n int) string {
	if n < 0 {
		return "-" + FormatNumber(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	offset := len(s) % 3
	if offset > 0 {
		b.WriteString(s[:offset])
	}
	for i := offset; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
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

// sanitizeLogMsg strips ANSI escape sequences, replaces tabs with spaces,
// and removes control characters that would corrupt terminal rendering
// (carriage returns, cursor movement, etc.).
func sanitizeLogMsg(s string) string {
	var b strings.Builder
	b.Grow(len(s))
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
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20 && r != '\n':
			// Drop control characters (\r, \b, etc.) but keep newline.
		default:
			b.WriteRune(r)
		}
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

// wrapText wraps a string into lines of the given width, breaking on rune boundaries.
func wrapText(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		runes := []rune(line)
		if len(runes) == 0 {
			lines = append(lines, "")
			continue
		}
		for len(runes) > width {
			lines = append(lines, string(runes[:width]))
			runes = runes[width:]
		}
		lines = append(lines, string(runes))
	}
	return lines
}

// formatLogLine renders a single log entry as a styled string.
// nameW is the fixed column width for the container name (0 = omit name column).
// Layout: "timestamp name │ message" or "timestamp │ message" when nameW=0.
func formatLogLine(entry protocol.LogEntryMsg, width int, theme *Theme, tsFormat string, nameW int) string {
	tsStr := FormatTimestamp(entry.Timestamp, tsFormat)
	tsW := lipgloss.Width(tsStr)
	muted := lipgloss.NewStyle().Foreground(theme.Muted)
	divider := muted.Render("│")

	// Left column width: ts + space + name + space (if nameW>0), or ts + space.
	leftW := tsW
	if nameW > 0 {
		leftW += 1 + nameW // space + name
	}

	// Synthetic lifecycle events: empty left column, message on the right.
	if entry.Stream == "event" {
		style := lipgloss.NewStyle().Foreground(theme.Warning)
		overhead := leftW + 3
		msgW := width - overhead
		if msgW < 10 {
			msgW = 10
		}
		return strings.Repeat(" ", leftW) + " " + divider + " " + style.Render(Truncate(sanitizeLogMsg(entry.Message), msgW))
	}

	ts := muted.Render(tsStr)

	var left string
	if nameW > 0 {
		nameRunes := []rune(entry.ContainerName)
		displayed := entry.ContainerName
		if len(nameRunes) > nameW {
			displayed = string(nameRunes[:nameW])
		}
		pad := nameW - len([]rune(displayed))
		nameColor := ContainerNameColor(entry.ContainerName, theme)
		name := lipgloss.NewStyle().Foreground(nameColor).Render(displayed)
		left = ts + " " + name + strings.Repeat(" ", pad)
	} else {
		left = ts
	}

	// left + space + │ + space = overhead before message.
	overhead := leftW + 3
	msgW := width - overhead
	if msgW < 10 {
		msgW = 10
	}
	msg := Truncate(sanitizeLogMsg(entry.Message), msgW)

	if entry.Stream == "stderr" {
		msg = lipgloss.NewStyle().Foreground(theme.Critical).Render(msg)
	}

	return left + " " + divider + " " + msg
}

// containerNameByID looks up a container name by ID.
func containerNameByID(id string, contInfo []protocol.ContainerInfo) string {
	for _, ci := range contInfo {
		if ci.ID == id {
			return ci.Name
		}
	}
	return ""
}
