package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Truncate shortens a plain (non-styled) string to maxLen, appending "..." if truncated.
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
func TruncateStyled(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxLen {
		return s
	}
	return ansi.Truncate(s, maxLen, "")
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

// FormatUptime formats seconds into a human-readable duration like "5d 11h".
func FormatUptime(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

// Overlay composites fg centered on top of bg. Both strings are
// newline-separated terminal renderings.
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

// renderBox renders a bordered box for modal overlays.
func renderBox(title, content string, width, height int, theme *Theme, titleStyleOverride ...*lipgloss.Style) string {
	if width < 4 {
		width = 4
	}
	if height < 3 {
		height = 3
	}

	innerW := width - 2
	borderStyle := lipgloss.NewStyle().Foreground(theme.Border)
	titleStyle := lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)
	if len(titleStyleOverride) > 0 && titleStyleOverride[0] != nil {
		titleStyle = *titleStyleOverride[0]
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

	lines := strings.Split(content, "\n")
	innerH := height - 2
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

// rightAlign right-pads a string with leading spaces to width w.
func rightAlign(s string, w int) string {
	n := len([]rune(s))
	if n >= w {
		return s
	}
	return strings.Repeat(" ", w-n) + s
}

func renderDivider(w int, theme *Theme) string {
	style := lipgloss.NewStyle().Foreground(theme.Border)
	return centerText(style.Render(strings.Repeat("─", w)), w)
}

func renderSpacedDivider(w int, theme *Theme) string {
	return "\n" + renderDivider(w, theme)
}

func renderLabeledDivider(label string, w int, theme *Theme) string {
	divStyle := lipgloss.NewStyle().Foreground(theme.Border)
	lblStyle := lipgloss.NewStyle().Foreground(theme.FgDim)

	lbl := " " + label + " "
	lblLen := len(lbl)
	side := (w - lblLen) / 2
	var line string
	if side < 1 {
		line = divStyle.Render(strings.Repeat("─", w))
	} else {
		right := w - side - lblLen
		line = divStyle.Render(strings.Repeat("─", side)) + lblStyle.Render(lbl) + divStyle.Render(strings.Repeat("─", right))
	}
	return "\n" + line
}

// centerText pads a styled string to center it within totalW.
func centerText(s string, totalW int) string {
	w := lipgloss.Width(s)
	if w >= totalW {
		return s
	}
	pad := (totalW - w) / 2
	return strings.Repeat(" ", pad) + s
}

// formatBytes formats a byte count into a compact human-readable string like "30.9M" or "1.2G".
func formatBytes(b uint64) string {
	switch {
	case b >= 1<<30:
		v := float64(b) / (1 << 30)
		if v >= 100 {
			return fmt.Sprintf("%.0fG", v)
		}
		if v >= 10 {
			return fmt.Sprintf("%.1fG", v)
		}
		return fmt.Sprintf("%.2fG", v)
	case b >= 1<<20:
		v := float64(b) / (1 << 20)
		if v >= 100 {
			return fmt.Sprintf("%.0fM", v)
		}
		if v >= 10 {
			return fmt.Sprintf("%.1fM", v)
		}
		return fmt.Sprintf("%.2fM", v)
	case b >= 1<<10:
		v := float64(b) / (1 << 10)
		if v >= 100 {
			return fmt.Sprintf("%.0fK", v)
		}
		if v >= 10 {
			return fmt.Sprintf("%.1fK", v)
		}
		return fmt.Sprintf("%.2fK", v)
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// formatCompactUptime formats seconds into a compact duration like "5d", "3h", "12m".
func formatCompactUptime(seconds int64) string {
	if seconds <= 0 {
		return "0m"
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	mins := (seconds % 3600) / 60
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dm", mins)
}

// renderHelpModal renders the help overlay for the current view.
func renderHelpModal(a *App, _ *Session, width, height int) string {
	theme := &a.theme
	fg := fgStyle(theme)
	dim := mutedStyle(theme)

	type binding struct{ key, desc string }
	var bindings []binding

	switch a.view {
	case viewDetail:
		bindings = []binding{
			{"esc", "back to dashboard"},
			{"j/k", "scroll logs"},
			{"h/l", "navigate filter fields"},
			{"G", "jump to latest"},
			{"enter", "expand log entry"},
			{"s", "cycle stream filter"},
			{"f", "open filter dialog"},
			{"+/-", "zoom time range"},
			{"i", "toggle info overlay"},
		}

	case viewAlerts:
		bindings = []binding{
			{"tab", "switch focus"},
			{"j/k", "navigate"},
			{"h/l", "navigate modal"},
			{"enter", "expand details"},
			{"a", "acknowledge alert"},
			{"s", "silence rule/alert"},
			{"r", "show/hide resolved"},
			{"g", "go to container"},
			{"1", "dashboard view"},
			{"q", "quit"},
		}

	default: // dashboard
		bindings = []binding{
			{"j/k", "navigate containers"},
			{"enter", "open detail view"},
			{"space", "expand/collapse project"},
			{"t", "track container"},
			{"+/-", "zoom time range"},
			{"S", "switch server"},
			{"2", "alerts view"},
			{"q", "quit"},
		}
	}

	const keyW = 12
	var lines []string
	for _, b := range bindings {
		keyStr := b.key
		for len(keyStr) < keyW {
			keyStr += " "
		}
		lines = append(lines, fg.Render(keyStr)+dim.Render(b.desc))
	}

	return (dialogLayout{
		title: "help",
		width: 40,
		lines: lines,
		tips:  dialogTips(theme, "esc", "close"),
	}).render(width, height, theme)
}

// dialogTips builds a footer tip string from alternating key-label pairs.
// Arguments: "a", "ack", "s", "silence", "esc", "close", ...
func dialogTips(theme *Theme, bindings ...string) string {
	fg := fgStyle(theme)
	muted := mutedStyle(theme)
	var parts []string
	for i := 0; i+1 < len(bindings); i += 2 {
		parts = append(parts, fg.Render(bindings[i])+" "+muted.Render(bindings[i+1]))
	}
	return strings.Join(parts, "  ")
}

// dialogLayout describes a centered modal dialog.
type dialogLayout struct {
	title      string
	titleStyle *lipgloss.Style // optional override for title color (default: theme.Accent)
	width      int             // desired modal width (clamped to terminal - 4)
	lines      []string        // content lines (unpadded, centered as a block)
	tips       string          // footer tip line (centered independently)
}

func (d dialogLayout) render(termW, termH int, theme *Theme) string {
	modalW := d.width
	if modalW > termW-4 {
		modalW = termW - 4
	}
	innerW := modalW - 2

	// Find max content width.
	maxW := 0
	for _, l := range d.lines {
		if w := lipgloss.Width(l); w > maxW {
			maxW = w
		}
	}
	leftPad := (innerW - maxW) / 2
	if leftPad < 2 {
		leftPad = 2
	}
	padStr := strings.Repeat(" ", leftPad)

	var padded []string
	padded = append(padded, "") // top blank line
	for _, l := range d.lines {
		if l == "" {
			padded = append(padded, "")
		} else {
			padded = append(padded, padStr+l)
		}
	}

	// Footer: 2 blank lines + centered tip line.
	tipPad := (innerW - lipgloss.Width(d.tips)) / 2
	if tipPad < 2 {
		tipPad = 2
	}
	padded = append(padded, "")
	padded = append(padded, "")
	padded = append(padded, strings.Repeat(" ", tipPad)+d.tips)

	content := strings.Join(padded, "\n")
	modalH := len(padded) + 2
	if modalH > termH-2 {
		modalH = termH - 2
	}

	return renderBox(d.title, content, modalW, modalH, theme, d.titleStyle)
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
