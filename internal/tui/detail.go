package tui

import (
	"context"
	"regexp"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
	logPaused bool

	expandModal *logExpandModal
	filterModal *logFilterModal

	// Filters.
	filterLevel string // "", "ERR", "WARN", "INFO", "DBUG"
	searchText  string
	searchRe    *regexp.Regexp
	filterFrom  int64
	filterTo    int64

	totalLogCount int

	cpuHist *RingBuffer[float64]
	memHist *RingBuffer[float64]

	backfilled             bool
	metricsBackfilled      bool
	metricsBackfillPending bool
	metricsGen             uint64

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
	entry       protocol.LogEntryMsg
	server      string
	project     string
	serviceName string
	scroll      int
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
	gen         uint64
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
	s.logPaused = false
	s.expandModal = nil
	s.filterModal = nil
	s.filterLevel = ""
	s.searchText = ""
	s.searchRe = nil
	s.filterFrom = 0
	s.filterTo = 0
	s.totalLogCount = 0
	s.cpuHist = NewRingBuffer[float64](histBufSize)
	s.memHist = NewRingBuffer[float64](histBufSize)
	s.backfilled = false
	s.metricsBackfilled = false
	s.metricsBackfillPending = false
	s.metricsGen = 0
	s.infoOverlay = false
}

func (s *DetailState) isGroupMode() bool {
	return s.project != "" && s.containerID == ""
}

func (s *DetailState) isSearchActive() bool {
	return s.searchText != "" || s.filterFrom != 0 || s.filterTo != 0 || s.filterLevel != ""
}

// setSearchText sets the search text and compiles a regex from it.
// Falls back to QuoteMeta on invalid regex.
func (s *DetailState) setSearchText(text string) {
	s.searchText = text
	if text == "" {
		s.searchRe = nil
		return
	}
	re, err := regexp.Compile("(?i)" + text)
	if err != nil {
		re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(text))
	}
	s.searchRe = re
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
		gen := s.metricsGen
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
				return detailMetricsQueryMsg{containerID: id, project: project, windowSec: ws, gen: gen}
			}
			return detailMetricsQueryMsg{resp: resp, containerID: id, project: project, windowSec: ws, gen: gen}
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
				s.totalLogCount++
				if s.logPaused {
					s.logScroll++
				}
				return
			}
		}
		return
	}
	if entry.ContainerID != s.containerID {
		return
	}
	s.logs.Push(entry)
	s.totalLogCount++
	if s.logPaused {
		s.logScroll++
	}
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
	if msg.gen != s.metricsGen {
		return // stale response, discard
	}
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
			sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })
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
	if s.filterLevel != "" && entry.Level != s.filterLevel {
		return false
	}
	if s.searchRe != nil && !s.searchRe.MatchString(entry.Message) {
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

func (s *DetailState) resetLogs() {
	s.logs = NewRingBuffer[protocol.LogEntryMsg](logBufCapacity)
	s.logScroll = 0
	s.logCursor = 0
	s.logPaused = false
	s.backfilled = false
}

func (s *DetailState) resetLogPosition() {
	s.logScroll = 0
	s.logPaused = false
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
	if s.filterLevel == "" && s.searchRe == nil && s.filterFrom == 0 && s.filterTo == 0 {
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

	// Check if we're re-entering the same container/project.
	sameTarget := false
	if item.isProject {
		g := a.groups[item.groupIdx]
		sameTarget = det.project == g.name
	} else {
		c := a.groups[item.groupIdx].containers[item.contIdx]
		sameTarget = det.containerID == c.ID
	}

	if !sameTarget {
		det.reset()
	}

	if item.isProject {
		g := a.groups[item.groupIdx]
		det.project = g.name
		det.projectIDs = make([]string, len(g.containers))
		for i, c := range g.containers {
			det.projectIDs[i] = c.ID
		}
		// Pre-compute max service name width for aligned log columns.
		det.maxSvcNameW = 0
		for _, c := range g.containers {
			name := c.Service
			if name == "" {
				name = c.Name
			}
			if n := len([]rune(name)); n > det.maxSvcNameW {
				det.maxSvcNameW = n
			}
		}
		if det.maxSvcNameW > 12 {
			det.maxSvcNameW = 12
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
