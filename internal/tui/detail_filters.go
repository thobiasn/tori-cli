package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/rook/internal/protocol"
)

func (s *DetailState) matchesFilter(entry protocol.LogEntryMsg) bool {
	if s.filterStream != "" && entry.Stream != s.filterStream {
		return false
	}
	if s.searchText != "" && !strings.Contains(strings.ToLower(entry.Message), strings.ToLower(s.searchText)) {
		return false
	}
	if s.filterFrom > 0 && entry.Timestamp < s.filterFrom {
		return false
	}
	if s.filterTo > 0 && entry.Timestamp > s.filterTo {
		return false
	}
	return true
}

// resetLogPosition resets scroll/cursor/expanded after a filter change,
// placing the cursor on the last (newest) matching entry.
func (s *DetailState) resetLogPosition() {
	s.logScroll = 0
	s.logExpanded = -1
	s.logCursor = len(s.filteredData()) - 1
	if s.logCursor < 0 {
		s.logCursor = 0
	}
}

func (s *DetailState) filteredData() []protocol.LogEntryMsg {
	if s.logs == nil {
		return nil
	}
	all := s.logs.Data()
	if s.filterStream == "" && s.searchText == "" && s.filterFrom == 0 && s.filterTo == 0 {
		return all
	}
	var out []protocol.LogEntryMsg
	for _, e := range all {
		if s.matchesFilter(e) {
			out = append(out, e)
		}
	}
	return out
}

func (s *DetailState) cycleProjectFilter(contInfo []protocol.ContainerInfo) {
	seen := make(map[string]bool)
	for _, ci := range contInfo {
		if ci.Project != "" {
			seen[ci.Project] = true
		}
	}
	if len(seen) == 0 {
		s.filterProject = ""
		return
	}
	projects := make([]string, 0, len(seen))
	for p := range seen {
		projects = append(projects, p)
	}
	sort.Strings(projects)

	if s.filterProject == "" {
		s.filterProject = projects[0]
		return
	}
	for i, p := range projects {
		if p == s.filterProject {
			if i+1 < len(projects) {
				s.filterProject = projects[i+1]
			} else {
				s.filterProject = ""
			}
			return
		}
	}
	s.filterProject = ""
}

// injectDeploySeparators detects container ID transitions in chronologically
// ordered log entries and inserts synthetic "redeployed" separator entries
// at each boundary.
func injectDeploySeparators(entries []protocol.LogEntryMsg) []protocol.LogEntryMsg {
	if len(entries) == 0 {
		return entries
	}
	out := make([]protocol.LogEntryMsg, 0, len(entries)+4)
	prevID := entries[0].ContainerID
	out = append(out, entries[0])
	for _, e := range entries[1:] {
		if e.ContainerID != prevID && e.Stream != "event" {
			out = append(out, protocol.LogEntryMsg{
				Timestamp:     e.Timestamp,
				ContainerID:   e.ContainerID,
				ContainerName: e.ContainerName,
				Stream:        "event",
				Message:       fmt.Sprintf("── %s redeployed ──", e.ContainerName),
			})
			prevID = e.ContainerID
		}
		out = append(out, e)
	}
	return out
}

func renderDetailLogs(s *DetailState, label string, showNames bool, width, height int, theme *Theme, focused bool, tsFormat string) string {
	boxH := height - 1 // leave room for shortcut footer
	innerH := boxH - 2
	if innerH < 1 {
		innerH = 1
	}
	innerW := width - 2

	data := s.filteredData()
	// Apply scroll.
	var visible []protocol.LogEntryMsg
	if len(data) <= innerH {
		visible = data
	} else {
		end := len(data) - s.logScroll
		if end < 0 {
			end = 0
		}
		start := end - innerH
		if start < 0 {
			start = 0
		}
		visible = data[start:end]
	}

	// Clamp cursor to visible range.
	cursorIdx := s.logCursor
	if cursorIdx >= len(visible) {
		cursorIdx = len(visible) - 1
		s.logCursor = cursorIdx
	}
	expandIdx := s.logExpanded
	var expandLines int
	if expandIdx >= 0 && expandIdx < len(visible) {
		expandLines = len(wrapText(visible[expandIdx].Message, innerW-2))
	}

	// If expansion would overflow, trim entries from the top.
	if expandLines > 0 && len(visible)+expandLines > innerH {
		trim := len(visible) + expandLines - innerH
		if trim > len(visible) {
			trim = len(visible)
		}
		visible = visible[trim:]
		cursorIdx -= trim
		expandIdx -= trim
	}

	var lines []string
	for i, entry := range visible {
		line := formatLogLine(entry, innerW, theme, tsFormat, showNames)
		if i == cursorIdx {
			line = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(line), innerW))
		}
		lines = append(lines, line)
		if i == expandIdx {
			wrapped := wrapText(entry.Message, innerW-2)
			for _, wl := range wrapped {
				lines = append(lines, "  "+wl)
			}
		}
	}

	title := "Logs"
	if label != "" {
		title += " ── " + label
	}
	title += " ── " + FormatNumber(len(data)) + " lines"
	paused := s.logScroll > 0
	if paused {
		title += " ── PAUSED"
	} else {
		title += " ── LIVE"
	}

	box := Box(title, strings.Join(lines, "\n"), width, boxH, theme, focused)
	return box + "\n" + renderDetailLogFooter(s, innerW, theme, tsFormat)
}

func renderDetailLogFooter(s *DetailState, width int, theme *Theme, tsFormat string) string {
	muted := lipgloss.NewStyle().Foreground(theme.Muted)

	var parts []string

	streamLabel := "all"
	if s.filterStream != "" {
		streamLabel = s.filterStream
	}
	parts = append(parts, "s: "+muted.Render(streamLabel))

	// Filter summary.
	if s.searchText != "" || s.filterFrom != 0 || s.filterTo != 0 {
		var filterParts []string
		if s.searchText != "" {
			filterParts = append(filterParts, fmt.Sprintf("%q", s.searchText))
		}
		if s.filterFrom != 0 || s.filterTo != 0 {
			from := "…"
			if s.filterFrom != 0 {
				from = time.Unix(s.filterFrom, 0).Format(tsFormat)
			}
			to := "…"
			if s.filterTo != 0 {
				to = time.Unix(s.filterTo, 0).Format(tsFormat)
			}
			filterParts = append(filterParts, from+"–"+to)
		}
		parts = append(parts, "f: "+strings.Join(filterParts, " "))
	} else {
		parts = append(parts, "f: filter")
	}

	parts = append(parts, "Esc clear")

	footer := " " + strings.Join(parts, " | ")
	return Truncate(footer, width)
}

// maskedField is a fixed-width input derived from a Go time format string.
// Digit positions in the format are editable; other characters are literal
// separators. Unfilled positions show defaults (current time) in muted style.
type maskedField struct {
	format   string
	slots    []rune // current display values (typed digits or defaults)
	defaults []rune // from time.Now().Format(format)
	editable []bool // true for digit positions
	typed    []bool // true for positions the user has actually edited
	cursor   int    // index in slots (always on editable position or past end)
	touched  bool   // true if any digit was typed
}

func newMaskedField(format string, now time.Time) maskedField {
	fmtRunes := []rune(format)
	defRunes := []rune(now.Format(format))
	n := len(fmtRunes)
	if len(defRunes) < n {
		n = len(defRunes)
	}

	slots := make([]rune, n)
	defaults := make([]rune, n)
	editable := make([]bool, n)
	typed := make([]bool, n)

	for i := 0; i < n; i++ {
		defaults[i] = defRunes[i]
		if fmtRunes[i] >= '0' && fmtRunes[i] <= '9' {
			editable[i] = true
			slots[i] = defRunes[i]
		} else {
			slots[i] = fmtRunes[i]
		}
	}

	cursor := 0
	for cursor < n && !editable[cursor] {
		cursor++
	}

	return maskedField{
		format:   format,
		slots:    slots,
		defaults: defaults,
		editable: editable,
		typed:    typed,
		cursor:   cursor,
	}
}

func (f *maskedField) typeRune(r rune) {
	if r < '0' || r > '9' || f.cursor >= len(f.slots) {
		return
	}
	f.slots[f.cursor] = r
	f.typed[f.cursor] = true
	f.touched = true
	f.cursor++
	for f.cursor < len(f.slots) && !f.editable[f.cursor] {
		f.cursor++
	}
}

func (f *maskedField) backspace() {
	pos := f.cursor - 1
	for pos >= 0 && !f.editable[pos] {
		pos--
	}
	if pos < 0 {
		return
	}
	f.slots[pos] = f.defaults[pos]
	f.typed[pos] = false
	f.cursor = pos

	f.touched = false
	for i := range f.editable {
		if f.editable[i] && f.typed[i] {
			f.touched = true
			break
		}
	}
}

// fill populates the field from a formatted string (for re-opening with existing values).
func (f *maskedField) fill(value string) {
	runes := []rune(value)
	for i := range f.slots {
		if i < len(runes) && f.editable[i] {
			f.slots[i] = runes[i]
			f.typed[i] = true
		}
	}
	f.touched = true
	f.cursor = len(f.slots)
}

// resolved returns the complete value if any position was typed, else "".
// Untyped positions are filled from defaults, so the result always matches the format.
func (f *maskedField) resolved() string {
	if !f.touched {
		return ""
	}
	return string(f.slots)
}

// render returns the display string. Typed positions are normal text,
// untyped positions are muted (showing defaults). The cursor position
// is rendered in accent color when focused.
func (f *maskedField) render(focused bool, theme *Theme) string {
	accent := lipgloss.NewStyle().Foreground(theme.Accent)
	muted := lipgloss.NewStyle().Foreground(theme.Muted)

	var b strings.Builder
	for i, s := range f.slots {
		ch := string(s)
		if focused && i == f.cursor {
			b.WriteString(accent.Render(ch))
		} else if f.editable[i] && !f.typed[i] {
			b.WriteString(muted.Render(ch))
		} else {
			b.WriteString(ch)
		}
	}
	return b.String()
}

// parseFilterBound builds a unix timestamp from separate date and time inputs.
// Each input is either "" (field untouched) or a complete formatted string.
// Returns 0 if both are empty.
func parseFilterBound(dateStr, timeStr, dateFormat, timeFormat string, isTo bool) int64 {
	if dateStr == "" && timeStr == "" {
		return 0
	}

	if dateStr != "" && timeStr != "" {
		full := dateFormat + " " + timeFormat
		if t, err := time.ParseInLocation(full, dateStr+" "+timeStr, time.Local); err == nil {
			return t.Unix()
		}
		return 0
	}

	if dateStr != "" {
		if t, err := time.ParseInLocation(dateFormat, dateStr, time.Local); err == nil {
			if isTo {
				return t.Add(24*time.Hour - time.Second).Unix()
			}
			return t.Unix()
		}
		return 0
	}

	// Time only — today's date.
	if t, err := time.ParseInLocation(timeFormat, timeStr, time.Local); err == nil {
		now := time.Now()
		combined := time.Date(now.Year(), now.Month(), now.Day(),
			t.Hour(), t.Minute(), t.Second(), 0, time.Local)
		return combined.Unix()
	}
	return 0
}

// renderFilterModal renders a centered filter modal overlay.
func renderFilterModal(m *logFilterModal, width, height int, theme *Theme, cfg DisplayConfig) string {
	dateW := len([]rune(cfg.DateFormat))
	timeW := len([]rune(cfg.TimeFormat))

	// Layout: "  From  [date]   [time]"
	//          ^^^^^^^^ = 8 char prefix
	const prefix = 8 // "  From  " or "  To    "
	lineW := prefix + 1 + dateW + 1 + 3 + 1 + timeW + 1
	modalW := lineW + 6 // borders + margin
	if modalW < 45 {
		modalW = 45
	}
	if modalW > width-4 {
		modalW = width - 4
	}
	innerW := modalW - 2

	cursor := lipgloss.NewStyle().Foreground(theme.Accent).Render("_")
	muted := lipgloss.NewStyle().Foreground(theme.Muted)

	textField := func(val string, focused bool) string {
		maxW := innerW - 4
		display := Truncate(val, maxW-1)
		if focused {
			return display + cursor
		}
		return display
	}

	// "date" and "time" headers aligned with the "[" of each field.
	pad := func(s string, w int) string {
		if len(s) >= w {
			return s[:w]
		}
		return s + strings.Repeat(" ", w-len(s))
	}
	// From "[" to "[": 1 + dateW + 1 + 3 = dateW + 5
	hdrDate := pad("date", dateW+5)

	var lines []string
	lines = append(lines, "")
	lines = append(lines, "  Text")
	lines = append(lines, "  ["+textField(m.text, m.focus == 0)+"]")
	lines = append(lines, "")
	lines = append(lines, strings.Repeat(" ", prefix)+muted.Render(hdrDate+"time"))
	lines = append(lines, "  From  ["+m.fromDate.render(m.focus == 1, theme)+"]   ["+m.fromTime.render(m.focus == 2, theme)+"]")
	lines = append(lines, "  To    ["+m.toDate.render(m.focus == 3, theme)+"]   ["+m.toTime.render(m.focus == 4, theme)+"]")
	lines = append(lines, strings.Repeat(" ", prefix)+muted.Render(pad(cfg.DateFormat, dateW+5)+cfg.TimeFormat))
	lines = append(lines, "")
	lines = append(lines, "  "+muted.Render("Tab next · Enter apply · Esc cancel"))

	content := strings.Join(lines, "\n")
	modalH := len(lines) + 2
	modal := Box("Filter", content, modalW, modalH, theme)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

// updateDetail handles keys in the detail view.
func updateDetail(a *App, s *Session, msg tea.KeyMsg) tea.Cmd {
	det := &s.Detail
	key := msg.String()

	// Filter modal captures all keys when open.
	if det.filterModal != nil {
		return updateFilterModal(det, key, a.displayCfg)
	}

	if key == "esc" {
		if det.searchText != "" || det.filterFrom != 0 || det.filterTo != 0 {
			det.searchText = ""
			det.filterFrom = 0
			det.filterTo = 0
			det.resetLogPosition()
		} else if det.filterStream != "" {
			det.filterStream = ""
			det.resetLogPosition()
		} else {
			a.active = viewDashboard
		}
		return nil
	}

	// Quick-toggle filter keys work regardless of focus.
	switch key {
	case "s":
		switch det.filterStream {
		case "":
			det.filterStream = "stdout"
		case "stdout":
			det.filterStream = "stderr"
		default:
			det.filterStream = ""
		}
		det.resetLogPosition()
		return nil
	case "g":
		det.cycleProjectFilter(s.ContInfo)
		det.resetLogPosition()
		return nil
	}

	// Open filter modal.
	if key == "f" {
		now := time.Now()
		m := &logFilterModal{
			text:     det.searchText,
			fromDate: newMaskedField(a.displayCfg.DateFormat, now),
			fromTime: newMaskedField(a.displayCfg.TimeFormat, now),
			toDate:   newMaskedField(a.displayCfg.DateFormat, now),
			toTime:   newMaskedField(a.displayCfg.TimeFormat, now),
		}
		if det.filterFrom != 0 {
			t := time.Unix(det.filterFrom, 0)
			m.fromDate.fill(t.Format(a.displayCfg.DateFormat))
			m.fromTime.fill(t.Format(a.displayCfg.TimeFormat))
		}
		if det.filterTo != 0 {
			t := time.Unix(det.filterTo, 0)
			m.toDate.fill(t.Format(a.displayCfg.DateFormat))
			m.toTime.fill(t.Format(a.displayCfg.TimeFormat))
		}
		det.filterModal = m
		return nil
	}

	data := det.filteredData()
	// Compute innerH for cursor bounds (same formula as renderDetail).
	contentH := a.height - 1
	metricsH := contentH / 3
	if metricsH < 11 {
		metricsH = 11
	}
	logH := contentH - metricsH - 1
	if logH < 5 {
		logH = 5
	}
	innerH := logH - 3 // box borders (2) + shortcut footer (1)
	if innerH < 1 {
		innerH = 1
	}

	visibleCount := len(data)
	if visibleCount > innerH {
		visibleCount = innerH
	}

	maxScroll := len(data) - innerH
	if maxScroll < 0 {
		maxScroll = 0
	}

	switch key {
	case "j", "down":
		if det.logCursor < visibleCount-1 {
			det.logCursor++
		} else if det.logScroll > 0 {
			det.logScroll--
		}
		det.logExpanded = -1
	case "k", "up":
		if det.logCursor > 0 {
			det.logCursor--
		} else if det.logScroll < maxScroll {
			det.logScroll++
		}
		det.logExpanded = -1
	case "enter":
		if det.logExpanded == det.logCursor {
			det.logExpanded = -1
		} else {
			det.logExpanded = det.logCursor
		}
	}
	return nil
}

// updateFilterModal handles keys inside the filter modal.
func updateFilterModal(det *DetailState, key string, cfg DisplayConfig) tea.Cmd {
	m := det.filterModal
	switch key {
	case "tab":
		m.focus = (m.focus + 1) % 5
	case "enter":
		det.searchText = m.text
		det.filterFrom = parseFilterBound(m.fromDate.resolved(), m.fromTime.resolved(), cfg.DateFormat, cfg.TimeFormat, false)
		det.filterTo = parseFilterBound(m.toDate.resolved(), m.toTime.resolved(), cfg.DateFormat, cfg.TimeFormat, true)
		det.filterModal = nil
		det.resetLogPosition()
	case "esc":
		det.filterModal = nil
	case "backspace":
		switch m.focus {
		case 0:
			if len(m.text) > 0 {
				m.text = m.text[:len(m.text)-1]
			}
		case 1:
			m.fromDate.backspace()
		case 2:
			m.fromTime.backspace()
		case 3:
			m.toDate.backspace()
		case 4:
			m.toTime.backspace()
		}
	default:
		if len(key) == 1 {
			switch m.focus {
			case 0:
				if len(m.text) < 128 {
					m.text += key
				}
			case 1:
				m.fromDate.typeRune(rune(key[0]))
			case 2:
				m.fromTime.typeRune(rune(key[0]))
			case 3:
				m.toDate.typeRune(rune(key[0]))
			case 4:
				m.toTime.typeRune(rune(key[0]))
			}
		}
	}
	return nil
}

