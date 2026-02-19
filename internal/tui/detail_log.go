package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

// injectDeploySeparators detects when a container name reappears with a
// different ID (i.e. it was redeployed) and inserts a synthetic separator.
// Simple container ID changes between different containers in a project
// are not redeploys and are ignored.
func injectDeploySeparators(entries []protocol.LogEntryMsg) []protocol.LogEntryMsg {
	if len(entries) == 0 {
		return entries
	}
	// Track the last-seen container ID per container name.
	seen := make(map[string]string) // name -> containerID
	out := make([]protocol.LogEntryMsg, 0, len(entries)+4)
	for _, e := range entries {
		if e.Stream == "event" {
			out = append(out, e)
			continue
		}
		prevID, known := seen[e.ContainerName]
		if known && prevID != e.ContainerID {
			out = append(out, protocol.LogEntryMsg{
				Timestamp:     e.Timestamp,
				ContainerID:   e.ContainerID,
				ContainerName: e.ContainerName,
				Stream:        "event",
				Message:       fmt.Sprintf("── %s redeployed ──", e.ContainerName),
			})
		}
		seen[e.ContainerName] = e.ContainerID
		out = append(out, e)
	}
	return out
}

func buildLogReq(det *DetailState, retDays int) *protocol.QueryLogsReq {
	now := time.Now().Unix()
	rangeSec := int64(86400)
	if retDays > 0 {
		rangeSec = int64(retDays) * 86400
	}
	req := &protocol.QueryLogsReq{
		Start: now - rangeSec,
		End:   now,
		Limit: logBufCapacity,
		Level: det.filterLevel,
	}
	if det.svcService != "" {
		req.Project = det.svcProject
		req.Service = det.svcService
	} else if det.project != "" {
		req.Project = det.project
	} else if len(det.projectIDs) > 0 {
		req.ContainerIDs = det.projectIDs
	} else if det.containerID != "" {
		req.ContainerID = det.containerID
	}
	return req
}

func fireSearch(det *DetailState, c *Client, retDays int) tea.Cmd {
	id := det.containerID
	project := det.project

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req := buildLogReq(det, retDays)
		req.Search = det.searchText
		if det.filterFrom > 0 {
			req.Start = det.filterFrom
		}
		if det.filterTo > 0 {
			req.End = det.filterTo
		}
		entries, total, err := c.QueryLogs(ctx, req)
		if err != nil {
			return detailLogQueryMsg{containerID: id, project: project}
		}
		return detailLogQueryMsg{entries: entries, total: total, containerID: id, project: project}
	}
}

func refetchLogs(det *DetailState, c *Client, retDays int) tea.Cmd {
	det.logs = NewRingBuffer[protocol.LogEntryMsg](logBufCapacity)
	det.logScroll = 0
	det.logCursor = 0
	det.logPaused = false
	det.backfilled = false
	det.totalLogCount = 0

	id := det.containerID
	project := det.project

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req := buildLogReq(det, retDays)
		entries, total, err := c.QueryLogs(ctx, req)
		if err != nil {
			return detailLogQueryMsg{containerID: id, project: project}
		}
		return detailLogQueryMsg{entries: entries, total: total, containerID: id, project: project}
	}
}

// maskedField is a fixed-width input derived from a Go time format string.
type maskedField struct {
	format   string
	slots    []rune
	defaults []rune
	editable []bool
	typed    []bool
	cursor   int
	touched  bool
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

func (f *maskedField) typeRune(r rune) bool {
	if r < '0' || r > '9' || f.cursor >= len(f.slots) {
		return false
	}
	f.slots[f.cursor] = r
	f.typed[f.cursor] = true
	f.touched = true
	f.cursor++
	for f.cursor < len(f.slots) && !f.editable[f.cursor] {
		f.cursor++
	}
	return f.cursor >= len(f.slots)
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

func (f *maskedField) resolved() string {
	if !f.touched {
		return ""
	}
	return string(f.slots)
}

func (f *maskedField) render(focused bool, theme *Theme) string {
	cursorStyle := lipgloss.NewStyle().Reverse(true)
	muted := mutedStyle(theme)

	var b strings.Builder
	for i, s := range f.slots {
		ch := string(s)
		if focused && i == f.cursor {
			b.WriteString(cursorStyle.Render(ch))
		} else if f.editable[i] && !f.typed[i] {
			b.WriteString(muted.Render(ch))
		} else {
			b.WriteString(ch)
		}
	}
	return b.String()
}

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

	if t, err := time.ParseInLocation(timeFormat, timeStr, time.Local); err == nil {
		now := time.Now()
		combined := time.Date(now.Year(), now.Month(), now.Day(),
			t.Hour(), t.Minute(), t.Second(), 0, time.Local)
		return combined.Unix()
	}
	return 0
}

// sanitizeLogMsg strips ANSI, replaces tabs, removes control characters.
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
			// Drop control characters but keep newline.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// formatTimestamp formats a unix timestamp using the given Go time layout.
func formatTimestamp(ts int64, format string) string {
	return time.Unix(ts, 0).Format(format)
}

// formatNumber formats an integer with comma separators.
func formatNumber(n int) string {
	if n < 0 {
		return "-" + formatNumber(-n)
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

// formatBytesRate formats bytes/second for display.
func formatBytesRate(bytesPerSec float64) string {
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

// countLines returns the number of visual lines in a rendered string.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// containerAlerts returns active alerts matching a container ID by InstanceKey suffix.
func containerAlerts(alerts map[int64]*protocol.AlertEvent, containerID string) []*protocol.AlertEvent {
	suffix := ":" + containerID
	var out []*protocol.AlertEvent
	for _, a := range alerts {
		if strings.HasSuffix(a.InstanceKey, suffix) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// findContainer returns the ContainerMetrics for a given ID, or nil.
func findContainer(id string, containers []protocol.ContainerMetrics) *protocol.ContainerMetrics {
	for i := range containers {
		if containers[i].ID == id {
			return &containers[i]
		}
	}
	return nil
}

func findContInfo(id string, contInfo []protocol.ContainerInfo) *protocol.ContainerInfo {
	for i := range contInfo {
		if contInfo[i].ID == id {
			return &contInfo[i]
		}
	}
	return nil
}

// serviceNameByID looks up the service name for a container ID from ContainerInfo.
// Falls back to container name, then empty string.
func serviceNameByID(id string, contInfo []protocol.ContainerInfo) string {
	for _, ci := range contInfo {
		if ci.ID == id {
			if ci.Service != "" {
				return ci.Service
			}
			return ci.Name
		}
	}
	return ""
}

// countDateChanges returns the number of date separator headers that would
// be rendered for the given entries (1 for the first entry + 1 per date change).
func countDateChanges(entries []protocol.LogEntryMsg, dateFormat string) int {
	if len(entries) == 0 {
		return 0
	}
	count := 1
	prev := time.Unix(entries[0].Timestamp, 0).Format(dateFormat)
	for _, e := range entries[1:] {
		d := time.Unix(e.Timestamp, 0).Format(dateFormat)
		if d != prev {
			count++
			prev = d
		}
	}
	return count
}

// visibleLogWindow returns the start and end indices into filtered data
// for the current scroll position, accounting for date header lines.
func visibleLogWindow(det *DetailState, data []protocol.LogEntryMsg, rawH int, dateFormat string) (int, int) {
	end := len(data) - det.logScroll
	if end < 0 {
		end = 0
	}
	if end > len(data) {
		end = len(data)
	}

	start := end - rawH
	if start < 0 {
		start = 0
	}

	// Reduce entries to make room for date headers.
	headers := countDateChanges(data[start:end], dateFormat)
	slots := rawH - headers
	if slots < 1 {
		slots = 1
	}
	start = end - slots
	if start < 0 {
		start = 0
	}

	return start, end
}
