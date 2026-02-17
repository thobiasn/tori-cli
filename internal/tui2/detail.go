package tui2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

// logBufCapacity is the number of log entries kept in the detail view ring buffer.
const logBufCapacity = 500

// DetailState holds state for the container/group detail view.
type DetailState struct {
	containerID string   // single-container mode (empty in group mode)
	project     string   // group mode (empty in single-container mode)
	projectIDs  []string // container IDs in the project (group mode)

	// Service identity for cross-container historical queries.
	svcProject string
	svcService string

	logs      *RingBuffer[protocol.LogEntryMsg]
	logScroll int
	logCursor int

	expandModal *logExpandModal
	filterModal *logFilterModal

	// Filters.
	filterStream string // "", "stdout", "stderr"
	searchText   string
	filterFrom   int64
	filterTo     int64

	totalLogCount int

	cpuHist *RingBuffer[float64]
	memHist *RingBuffer[float64]

	backfilled             bool
	metricsBackfilled      bool
	metricsBackfillPending bool

	infoOverlay bool

	// Pre-computed max service name width for project view log alignment.
	maxSvcNameW int
}

type logFilterModal struct {
	focus    int
	text     string
	fromDate maskedField
	fromTime maskedField
	toDate   maskedField
	toTime   maskedField
}

type logExpandModal struct {
	entry   protocol.LogEntryMsg
	server  string
	project string
	scroll  int
}

type detailLogQueryMsg struct {
	entries     []protocol.LogEntryMsg
	total       int
	containerID string
	project     string
}

type detailMetricsQueryMsg struct {
	resp        *protocol.QueryMetricsResp
	containerID string
	project     string
	windowSec   int64
}

func (s *DetailState) reset() {
	s.containerID = ""
	s.project = ""
	s.projectIDs = nil
	s.svcProject = ""
	s.svcService = ""
	s.maxSvcNameW = 0
	s.logs = NewRingBuffer[protocol.LogEntryMsg](logBufCapacity)
	s.logScroll = 0
	s.logCursor = 0
	s.expandModal = nil
	s.filterModal = nil
	s.filterStream = ""
	s.searchText = ""
	s.filterFrom = 0
	s.filterTo = 0
	s.totalLogCount = 0
	s.cpuHist = NewRingBuffer[float64](histBufSize)
	s.memHist = NewRingBuffer[float64](histBufSize)
	s.backfilled = false
	s.metricsBackfilled = false
	s.metricsBackfillPending = false
	s.infoOverlay = false
}

func (s *DetailState) isGroupMode() bool {
	return s.project != "" && s.containerID == ""
}

func (s *DetailState) isSearchActive() bool {
	return s.searchText != "" || s.filterFrom != 0 || s.filterTo != 0
}

func (s *DetailState) onSwitch(c *Client, windowSec int64, retentionDays int) tea.Cmd {
	if s.containerID == "" && s.project == "" {
		return nil
	}
	if s.logs == nil {
		s.reset()
	}

	var cmds []tea.Cmd

	// Subscribe to logs for this container/project.
	sub := &protocol.SubscribeLogs{}
	if s.isGroupMode() {
		sub.Project = s.project
	} else {
		sub.ContainerID = s.containerID
	}
	if err := c.Subscribe(protocol.TypeSubscribeLogs, sub); err != nil {
		// Non-fatal: streaming won't work but backfill still will.
	}

	// Log backfill.
	if !s.backfilled {
		id := s.containerID
		project := s.project
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req := buildLogReq(s, retentionDays)
			entries, total, err := c.QueryLogs(ctx, req)
			if err != nil {
				return detailLogQueryMsg{containerID: id, project: project}
			}
			return detailLogQueryMsg{entries: entries, total: total, containerID: id, project: project}
		})
	}

	// Metrics backfill.
	if !s.metricsBackfilled && (s.svcService != "" || s.project != "") {
		s.metricsBackfillPending = true
		id := s.containerID
		project := s.project
		svcProject := s.svcProject
		svcService := s.svcService
		ws := windowSec
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			now := time.Now().Unix()
			rangeSec := int64(histBufSize * 10)
			points := 0
			if ws > 0 {
				rangeSec = ws
				points = histBufSize
			}
			start := now - rangeSec
			req := &protocol.QueryMetricsReq{
				Start:  start,
				End:    now,
				Points: points,
			}
			if svcService != "" {
				req.Project = svcProject
				req.Service = svcService
			} else {
				req.Project = project
			}
			resp, err := c.QueryMetrics(ctx, req)
			if err != nil {
				return detailMetricsQueryMsg{containerID: id, project: project, windowSec: ws}
			}
			return detailMetricsQueryMsg{resp: resp, containerID: id, project: project, windowSec: ws}
		})
	}

	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func (s *DetailState) onStreamEntry(entry protocol.LogEntryMsg) {
	if s.logs == nil || s.isSearchActive() {
		return
	}
	if s.isGroupMode() {
		for _, id := range s.projectIDs {
			if entry.ContainerID == id {
				s.logs.Push(entry)
				return
			}
		}
		return
	}
	if entry.ContainerID != s.containerID {
		return
	}
	s.logs.Push(entry)
}

func (s *DetailState) handleBackfill(msg detailLogQueryMsg) {
	if s.backfilled || s.logs == nil {
		s.backfilled = true
		return
	}
	if msg.containerID != s.containerID || msg.project != s.project {
		return
	}

	s.totalLogCount = msg.total

	if len(msg.entries) == 0 {
		s.backfilled = true
		return
	}

	// Reverse from DESC (newest-first) to ASC (oldest-first).
	for i, j := 0, len(msg.entries)-1; i < j; i, j = i+1, j-1 {
		msg.entries[i], msg.entries[j] = msg.entries[j], msg.entries[i]
	}

	existing := s.logs.Data()
	var oldestTS int64
	if len(existing) > 0 {
		oldestTS = existing[0].Timestamp
	}

	backfill := injectDeploySeparators(msg.entries)

	newBuf := NewRingBuffer[protocol.LogEntryMsg](logBufCapacity)
	for _, e := range backfill {
		if oldestTS == 0 || e.Timestamp < oldestTS {
			newBuf.Push(e)
		}
	}
	for _, e := range existing {
		newBuf.Push(e)
	}
	s.logs = newBuf
	s.backfilled = true

	n := newBuf.Len()
	if n > 0 {
		s.logCursor = n - 1
	}
}

func (s *DetailState) handleMetricsBackfill(msg detailMetricsQueryMsg) {
	s.metricsBackfillPending = false
	if msg.containerID != s.containerID || msg.project != s.project {
		return
	}
	if msg.resp == nil {
		s.metricsBackfilled = true
		return
	}

	cpuBuf := NewRingBuffer[float64](histBufSize)
	memBuf := NewRingBuffer[float64](histBufSize)

	if s.isGroupMode() {
		// Aggregate across all containers in the project.
		if len(msg.resp.Containers) > 0 {
			// Group by timestamp index — containers are interleaved.
			type point struct {
				cpu float64
				mem float64
			}
			points := make(map[int64]*point)
			var timestamps []int64
			for _, cm := range msg.resp.Containers {
				p, ok := points[cm.Timestamp]
				if !ok {
					p = &point{}
					points[cm.Timestamp] = p
					timestamps = append(timestamps, cm.Timestamp)
				}
				p.cpu += cm.CPUPercent
				p.mem += float64(cm.MemUsage)
			}
			// Sort timestamps.
			for i := 1; i < len(timestamps); i++ {
				for j := i; j > 0 && timestamps[j-1] > timestamps[j]; j-- {
					timestamps[j-1], timestamps[j] = timestamps[j], timestamps[j-1]
				}
			}
			for _, ts := range timestamps {
				p := points[ts]
				cpuBuf.Push(p.cpu)
				memBuf.Push(p.mem)
			}
		}
	} else {
		for _, cm := range msg.resp.Containers {
			cpuBuf.Push(cm.CPUPercent)
			memBuf.Push(float64(cm.MemUsage))
		}
	}

	s.cpuHist = cpuBuf
	s.memHist = memBuf
	s.metricsBackfilled = true
}

func (s *DetailState) pushLiveMetrics(containers []protocol.ContainerMetrics) {
	if s.cpuHist == nil || s.memHist == nil {
		return
	}
	if s.isGroupMode() {
		var cpuSum float64
		var memSum float64
		for _, id := range s.projectIDs {
			for _, c := range containers {
				if c.ID == id {
					cpuSum += c.CPUPercent
					memSum += float64(c.MemUsage)
					break
				}
			}
		}
		s.cpuHist.Push(cpuSum)
		s.memHist.Push(memSum)
	} else {
		for _, c := range containers {
			if c.ID == s.containerID {
				s.cpuHist.Push(c.CPUPercent)
				s.memHist.Push(float64(c.MemUsage))
				return
			}
		}
	}
}

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

func (s *DetailState) resetLogPosition() {
	s.logScroll = 0
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

// injectDeploySeparators detects container ID transitions and inserts
// synthetic "redeployed" separator entries at each boundary.
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

// enterDetail populates DetailState and switches to detail view.
func (a *App) enterDetail() (App, tea.Cmd) {
	s := a.session()
	if s == nil || s.Client == nil {
		return *a, nil
	}

	items := buildSelectableItems(a.groups, a.collapsed)
	if a.cursor < 0 || a.cursor >= len(items) {
		return *a, nil
	}

	item := items[a.cursor]
	det := &s.Detail
	det.reset()

	if item.isProject {
		g := a.groups[item.groupIdx]
		det.project = g.name
		det.projectIDs = make([]string, len(g.containers))
		for i, c := range g.containers {
			det.projectIDs[i] = c.ID
		}
		// Pre-compute max service name width for aligned log columns.
		for _, c := range g.containers {
			name := c.Service
			if name == "" {
				name = c.Name
			}
			if n := len([]rune(name)); n > det.maxSvcNameW {
				det.maxSvcNameW = n
			}
		}
		if det.maxSvcNameW > 20 {
			det.maxSvcNameW = 20
		}
	} else {
		c := a.groups[item.groupIdx].containers[item.contIdx]
		det.containerID = c.ID
		// Set service identity for cross-container metrics backfill.
		for _, ci := range s.ContInfo {
			if ci.ID == c.ID {
				if ci.Service != "" {
					det.svcProject = ci.Project
					det.svcService = ci.Service
				} else {
					det.svcService = ci.Name
				}
				break
			}
		}
	}

	a.view = viewDetail
	cmd := det.onSwitch(s.Client, a.windowSeconds(), s.RetentionDays)
	return *a, cmd
}

// leaveDetail unsubscribes from logs and returns to dashboard.
func (a *App) leaveDetail() tea.Cmd {
	a.view = viewDashboard
	s := a.session()
	if s != nil && s.Client != nil {
		_ = s.Client.Unsubscribe("logs")
	}
	return nil
}

// handleDetailKey handles keys when the detail view is active.
func (a *App) handleDetailKey(msg tea.KeyMsg) (App, tea.Cmd) {
	s := a.session()
	if s == nil {
		return *a, nil
	}
	det := &s.Detail
	key := msg.String()

	// Expand modal captures all keys.
	if det.expandModal != nil {
		return *a, updateExpandModal(det, a, s, key)
	}

	// Filter modal captures all keys.
	if det.filterModal != nil {
		return *a, updateFilterModal(det, s, key, a.display)
	}

	// Info overlay: dismiss with i or Esc.
	if det.infoOverlay {
		if key == "i" || key == "esc" {
			det.infoOverlay = false
		}
		return *a, nil
	}

	// Zoom.
	if key == "+" || key == "=" || key == "-" {
		if cmd := a.handleZoom(key); cmd != nil {
			return *a, cmd
		}
		return *a, nil
	}

	switch key {
	case "esc":
		if det.isSearchActive() {
			det.searchText = ""
			det.filterFrom = 0
			det.filterTo = 0
			return *a, refetchLogs(det, s.Client, s.RetentionDays)
		}
		if det.filterStream != "" {
			det.filterStream = ""
			det.resetLogPosition()
			return *a, nil
		}
		return *a, a.leaveDetail()

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
		return *a, nil

	case "f":
		now := time.Now()
		m := &logFilterModal{
			text:     det.searchText,
			fromDate: newMaskedField(a.display.DateFormat, now),
			fromTime: newMaskedField(a.display.TimeFormat, now),
			toDate:   newMaskedField(a.display.DateFormat, now),
			toTime:   newMaskedField(a.display.TimeFormat, now),
		}
		if det.filterFrom != 0 {
			t := time.Unix(det.filterFrom, 0)
			m.fromDate.fill(t.Format(a.display.DateFormat))
			m.fromTime.fill(t.Format(a.display.TimeFormat))
		}
		if det.filterTo != 0 {
			t := time.Unix(det.filterTo, 0)
			m.toDate.fill(t.Format(a.display.DateFormat))
			m.toTime.fill(t.Format(a.display.TimeFormat))
		}
		det.filterModal = m
		return *a, nil

	case "i":
		if !det.isGroupMode() {
			det.infoOverlay = true
		}
		return *a, nil

	case "G":
		det.logScroll = 0
		data := det.filteredData()
		if len(data) > 0 {
			det.logCursor = len(data) - 1
		}
		return *a, nil
	}

	// Log scrolling.
	data := det.filteredData()
	innerH := logAreaHeight(a, det, s)
	start, end := visibleLogWindow(det, data, innerH, a.display.DateFormat)
	visibleCount := end - start
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
	case "k", "up":
		if det.logCursor > 0 {
			det.logCursor--
		} else if det.logScroll < maxScroll {
			det.logScroll++
		}
	case "enter":
		if det.logCursor >= 0 && len(data) > 0 {
			idx := start + det.logCursor
			if idx >= 0 && idx < len(data) {
				project := det.project
				if project == "" {
					project = det.svcProject
				}
				det.expandModal = &logExpandModal{
					entry:   data[idx],
					server:  s.Name,
					project: project,
				}
			}
		}
	}
	return *a, nil
}

// updateFilterModal handles keys inside the filter modal.
func updateFilterModal(det *DetailState, s *Session, key string, cfg DisplayConfig) tea.Cmd {
	m := det.filterModal
	switch key {
	case "tab":
		if m.focus == 0 {
			m.focus = 1
		} else {
			m.focus = 0
		}
	case "enter":
		det.searchText = m.text
		det.filterFrom = parseFilterBound(m.fromDate.resolved(), m.fromTime.resolved(), cfg.DateFormat, cfg.TimeFormat, false)
		det.filterTo = parseFilterBound(m.toDate.resolved(), m.toTime.resolved(), cfg.DateFormat, cfg.TimeFormat, true)
		det.filterModal = nil

		if det.isSearchActive() {
			det.logs = NewRingBuffer[protocol.LogEntryMsg](logBufCapacity)
			det.logScroll = 0
			det.logCursor = 0
			det.backfilled = false
			return fireSearch(det, s.Client, s.RetentionDays)
		}
		return refetchLogs(det, s.Client, s.RetentionDays)
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
		if m.focus > 0 {
			switch key {
			case "h", "left":
				if m.focus == 2 {
					m.focus = 1
				} else if m.focus == 4 {
					m.focus = 3
				}
				return nil
			case "l", "right":
				if m.focus == 1 {
					m.focus = 2
				} else if m.focus == 3 {
					m.focus = 4
				}
				return nil
			case "j", "down":
				if m.focus == 1 {
					m.focus = 3
				} else if m.focus == 2 {
					m.focus = 4
				}
				return nil
			case "k", "up":
				if m.focus == 3 {
					m.focus = 1
				} else if m.focus == 4 {
					m.focus = 2
				}
				return nil
			}
		}
		if len(key) == 1 {
			switch m.focus {
			case 0:
				if len(m.text) < 128 {
					m.text += key
				}
			case 1:
				if m.fromDate.typeRune(rune(key[0])) {
					m.focus = 2
				}
			case 2:
				if m.fromTime.typeRune(rune(key[0])) {
					m.focus = 3
				}
			case 3:
				if m.toDate.typeRune(rune(key[0])) {
					m.focus = 4
				}
			case 4:
				m.toTime.typeRune(rune(key[0]))
			}
		}
	}
	return nil
}

// updateExpandModal handles keys inside the log expand modal.
func updateExpandModal(det *DetailState, a *App, s *Session, key string) tea.Cmd {
	m := det.expandModal
	switch key {
	case "esc", "enter":
		det.expandModal = nil
	case "n":
		m.scroll++
	case "p":
		if m.scroll > 0 {
			m.scroll--
		}
	case "j", "k", "down", "up":
		data := det.filteredData()
		if len(data) == 0 {
			return nil
		}
		innerH := logAreaHeight(a, det, s)
		start, end := visibleLogWindow(det, data, innerH, a.display.DateFormat)
		visibleCount := end - start
		idx := start + det.logCursor
		if key == "j" || key == "down" {
			if idx+1 >= len(data) {
				return nil
			}
			idx++
			if det.logCursor < visibleCount-1 {
				det.logCursor++
			} else if det.logScroll > 0 {
				det.logScroll--
			}
		} else {
			if idx <= 0 {
				return nil
			}
			idx--
			maxScroll := len(data) - innerH
			if maxScroll < 0 {
				maxScroll = 0
			}
			if det.logCursor > 0 {
				det.logCursor--
			} else if det.logScroll < maxScroll {
				det.logScroll++
			}
		}
		m.entry = data[idx]
		m.scroll = 0
	}
	return nil
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
	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

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

// parsedLog holds the extracted message and level from a raw log line.
type parsedLog struct {
	message string
	level   string
}

// parseLogMessage tries to extract a clean message and level from a raw log line.
// Priority: JSON → logfmt → raw.
func parseLogMessage(raw string) parsedLog {
	// Try JSON.
	if len(raw) > 0 && raw[0] == '{' {
		var m map[string]interface{}
		if json.Unmarshal([]byte(raw), &m) == nil {
			p := parsedLog{message: raw}
			for _, k := range []string{"level", "lvl"} {
				if v, ok := m[k]; ok {
					p.level = normalizeLevel(fmt.Sprint(v))
					break
				}
			}
			for _, k := range []string{"msg", "message", "error"} {
				if v, ok := m[k]; ok {
					p.message = fmt.Sprint(v)
					break
				}
			}
			return p
		}
	}

	// Try logfmt (key=value pairs).
	if strings.ContainsRune(raw, '=') {
		if p := parseLogfmt(raw); p.message != "" || p.level != "" {
			if p.message == "" {
				p.message = raw
			}
			return p
		}
	}

	return parsedLog{message: raw}
}

// parseLogfmt extracts msg/message and level/lvl from logfmt-style key=value pairs.
func parseLogfmt(raw string) parsedLog {
	var p parsedLog
	i := 0
	for i < len(raw) {
		for i < len(raw) && raw[i] == ' ' {
			i++
		}
		if i >= len(raw) {
			break
		}

		keyStart := i
		for i < len(raw) && raw[i] != '=' && raw[i] != ' ' {
			i++
		}
		if i >= len(raw) || raw[i] != '=' {
			for i < len(raw) && raw[i] != ' ' {
				i++
			}
			continue
		}
		key := raw[keyStart:i]
		i++ // skip '='

		var val string
		if i < len(raw) && raw[i] == '"' {
			i++ // skip opening quote
			valStart := i
			for i < len(raw) && raw[i] != '"' {
				if raw[i] == '\\' && i+1 < len(raw) {
					i++
				}
				i++
			}
			val = raw[valStart:i]
			if i < len(raw) {
				i++ // skip closing quote
			}
		} else {
			valStart := i
			for i < len(raw) && raw[i] != ' ' {
				i++
			}
			val = raw[valStart:i]
		}

		switch strings.ToLower(key) {
		case "msg", "message":
			p.message = val
		case "level", "lvl":
			p.level = normalizeLevel(val)
		}
	}
	return p
}

// normalizeLevel normalizes a log level string to a standard form.
func normalizeLevel(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "INFO", "INFORMATION":
		return "INFO"
	case "WARN", "WARNING":
		return "WARN"
	case "ERR", "ERROR":
		return "ERROR"
	case "DEBUG", "DBG", "TRACE":
		return "DEBUG"
	case "FATAL", "PANIC":
		return "ERROR"
	default:
		return ""
	}
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

// renderExpandModal renders a centered overlay showing the full log message.
func renderExpandModal(m *logExpandModal, width, height int, theme *Theme, tsFormat string) string {
	modalW := width * 3 / 4
	if modalW < 40 {
		modalW = 40
	}
	if modalW > width-4 {
		modalW = width - 4
	}
	modalH := height * 2 / 3
	if modalH < 10 {
		modalH = 10
	}
	innerW := modalW - 2
	innerH := modalH - 2
	if innerH < 1 {
		innerH = 1
	}

	muted := lipgloss.NewStyle().Foreground(theme.FgDim)

	footerLine := " " + muted.Render("j/k Next/Prev  n/p Scroll  Esc Close")

	var header []string
	header = append(header, muted.Render(" server:    ")+m.server)
	if m.project != "" {
		header = append(header, muted.Render(" project:   ")+m.project)
	}
	header = append(header, muted.Render(" container: ")+m.entry.ContainerName)
	header = append(header, "")
	header = append(header, muted.Render(" timestamp: ")+formatTimestamp(m.entry.Timestamp, tsFormat))
	header = append(header, muted.Render(" stream:    ")+m.entry.Stream)
	header = append(header, " "+muted.Render(strings.Repeat("─", innerW-2)))

	contentH := innerH - len(header) - 1
	if contentH < 1 {
		contentH = 1
	}

	msg := sanitizeLogMsg(m.entry.Message)
	if json.Valid([]byte(msg)) {
		var buf bytes.Buffer
		if json.Indent(&buf, []byte(msg), "", "  ") == nil {
			msg = buf.String()
		}
	}

	wrapped := wrapText(msg, innerW-2)
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}

	maxScroll := len(wrapped) - contentH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}

	start := m.scroll
	end := start + contentH
	if end > len(wrapped) {
		end = len(wrapped)
	}

	var lines []string
	lines = append(lines, header...)
	for _, l := range wrapped[start:end] {
		lines = append(lines, " "+l)
	}
	used := len(lines) + 1
	for i := used; i < innerH; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, footerLine)

	content := strings.Join(lines, "\n")
	return renderBox("Log", content, modalW, modalH, theme)
}
