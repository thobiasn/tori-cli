package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/rook/internal/protocol"
)

// DetailState holds state for the container/group detail view.
type DetailState struct {
	containerID string   // single-container mode (empty in group mode)
	project     string   // group mode (empty in single-container mode)
	projectIDs  []string // container IDs in the project (group mode)

	// Service identity for cross-container historical queries.
	// Set when entering single-container detail; empty for group mode.
	svcProject string // compose project (or "" for non-compose)
	svcService string // compose service label (or container name for non-compose)

	logs      *RingBuffer[protocol.LogEntryMsg]
	logScroll int
	logCursor int // index in visible entries (always ≥ 0)

	expandModal *logExpandModal // nil = closed

	// Filters.
	filterProject string // project filter (g key, only meaningful in group mode)
	filterStream      string // "", "stdout", "stderr"
	searchText        string // applied text filter
	filterFrom        int64  // applied from timestamp (0 = no filter)
	filterTo          int64  // applied to timestamp (0 = no filter)
	filterModal       *logFilterModal

	backfilled             bool
	metricsBackfilled      bool
	metricsBackfillPending bool // true while a detail metrics backfill is in-flight
}

// logFilterModal holds the transient state of the filter modal while open.
type logFilterModal struct {
	focus    int    // 0=text, 1=fromDate, 2=fromTime, 3=toDate, 4=toTime
	text     string
	fromDate maskedField
	fromTime maskedField
	toDate   maskedField
	toTime   maskedField
}

// logExpandModal holds state for the full-message expand overlay.
type logExpandModal struct {
	entry   protocol.LogEntryMsg
	server  string
	project string
	scroll  int
}

type detailLogQueryMsg struct {
	entries     []protocol.LogEntryMsg
	containerID string // which detail view requested this
	project     string
}

type detailMetricsQueryMsg struct {
	resp        *protocol.QueryMetricsResp
	containerID string
	project     string
	start       int64
	end         int64
	windowSec   int64
}

func (s *DetailState) reset() {
	s.logs = NewRingBuffer[protocol.LogEntryMsg](5000)
	s.logScroll = 0
	s.logCursor = 0
	s.expandModal = nil
	s.filterProject = ""
	s.filterStream = ""
	s.searchText = ""
	s.filterFrom = 0
	s.filterTo = 0
	s.filterModal = nil
	s.backfilled = false
	s.metricsBackfilled = false
	s.metricsBackfillPending = false
}

func (s *DetailState) isGroupMode() bool {
	return s.project != "" && s.containerID == ""
}

func (s *DetailState) onSwitch(c *Client, windowSec int64, retentionDays int) tea.Cmd {
	if s.containerID == "" && s.project == "" {
		return nil
	}
	if s.logs == nil {
		s.reset()
	}

	var cmds []tea.Cmd

	// Log backfill.
	if !s.backfilled {
		id := s.containerID
		project := s.project
		ids := s.projectIDs
		svcProject := s.svcProject
		svcService := s.svcService
		retDays := retentionDays
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			now := time.Now().Unix()
			rangeSec := int64(86400) // default 1 day
			if retDays > 0 {
				rangeSec = int64(retDays) * 86400
			}
			req := &protocol.QueryLogsReq{
				Start: now - rangeSec,
				End:   now,
				Limit: 5000,
			}
			// Prefer service/project identity for cross-container history.
			if svcService != "" {
				req.Project = svcProject
				req.Service = svcService
			} else if project != "" {
				req.Project = project
			} else if len(ids) > 0 {
				req.ContainerIDs = ids
			} else if id != "" {
				req.ContainerID = id
			}
			entries, err := c.QueryLogs(ctx, req)
			if err != nil {
				return detailLogQueryMsg{containerID: id, project: project}
			}
			return detailLogQueryMsg{entries: entries, containerID: id, project: project}
		})
	}

	// Metrics backfill for cross-container graph history.
	// Fires for single-container mode (service identity) or group mode (project).
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
			rangeSec := int64(ringBufSize * 10)
			points := 0
			if ws > 0 {
				rangeSec = ws
				points = ringBufSize
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
				return detailMetricsQueryMsg{containerID: id, project: project, start: start, end: now, windowSec: ws}
			}
			return detailMetricsQueryMsg{resp: resp, containerID: id, project: project, start: start, end: now, windowSec: ws}
		})
	}

	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func (s *DetailState) onStreamEntry(entry protocol.LogEntryMsg) {
	if s.logs == nil {
		return
	}
	if s.isGroupMode() {
		// Accept entries from any container in the project.
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
	// Reject stale responses from a previous detail view.
	if msg.containerID != s.containerID || msg.project != s.project {
		return
	}
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

	// Inject deploy separators at container ID transitions in backfilled data.
	backfill := injectDeploySeparators(msg.entries)

	newBuf := NewRingBuffer[protocol.LogEntryMsg](5000)
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

	// Pin cursor to the latest entry.
	n := newBuf.Len()
	if n > 0 {
		s.logCursor = n - 1
	}
}

// renderDetail renders the container/group detail full-screen view.
func renderDetail(a *App, s *Session, width, height int) string {
	theme := &a.theme
	det := &s.Detail

	if det.containerID == "" && det.project == "" {
		return Box("Detail", "  No container selected. Press Enter on a container in the dashboard.", width, height, theme)
	}

	if det.isGroupMode() {
		return renderDetailGroup(a, s, width, height)
	}
	return renderDetailSingle(a, s, width, height)
}

func renderDetailSingle(a *App, s *Session, width, height int) string {
	theme := &a.theme
	det := &s.Detail

	// Find current container metrics.
	var cm *protocol.ContainerMetrics
	for i := range s.Containers {
		if s.Containers[i].ID == det.containerID {
			cm = &s.Containers[i]
			break
		}
	}

	// Find container info.
	var ci *protocol.ContainerInfo
	for i := range s.ContInfo {
		if s.ContInfo[i].ID == det.containerID {
			ci = &s.ContInfo[i]
			break
		}
	}

	// Title: "name — state — N alerts"
	title := "Detail"
	if ci != nil {
		title = stripANSI(ci.Name)
		if cm != nil {
			stateInd := theme.StateIndicator(cm.State)
			title += " ── " + stateInd + " " + stripANSI(cm.State)
		}
	} else if cm != nil {
		stateInd := theme.StateIndicator(cm.State)
		title = stripANSI(cm.Name) + " ── " + stateInd + " " + stripANSI(cm.State)
	}
	// Show alert count in title.
	alertCount := len(containerAlerts(s.Alerts, det.containerID))
	if alertCount > 0 {
		title += fmt.Sprintf(" ── %d alert", alertCount)
		if alertCount > 1 {
			title += "s"
		}
	}

	// 1/3 metrics, 2/3 logs.
	metricsH := height / 3
	if metricsH < 11 {
		metricsH = 11
	}
	logH := height - metricsH - 1
	if logH < 5 {
		metricsH = height - 6
		logH = 5
	}

	rc := RenderContext{Width: width, Height: metricsH, Theme: theme, WindowLabel: a.windowLabel(), WindowSec: a.windowSeconds()}
	metricsContent := renderDetailMetrics(s, det, cm, rc)
	metricsBox := Box(title, metricsContent, width, metricsH, theme)

	// Bottom section: logs.
	containerName := ""
	if ci != nil {
		containerName = stripANSI(ci.Name)
	} else if cm != nil {
		containerName = stripANSI(cm.Name)
	}
	var logBox string
	if det.logs != nil && logH > 3 {
		logBox = renderDetailLogs(det, containerName, false, width, logH, theme, true, a.tsFormat())
	}

	return metricsBox + "\n" + logBox
}

func renderDetailGroup(a *App, s *Session, width, height int) string {
	theme := &a.theme
	det := &s.Detail

	// Build title: "project — N/M running — K alerts"
	total := len(det.projectIDs)
	running := 0
	for _, id := range det.projectIDs {
		for _, c := range s.Containers {
			if c.ID == id && c.State == "running" {
				running++
				break
			}
		}
	}
	title := det.project + fmt.Sprintf(" ── %d/%d running", running, total)

	// Count alerts for all containers in project.
	alertCount := 0
	for _, id := range det.projectIDs {
		alertCount += len(containerAlerts(s.Alerts, id))
	}
	if alertCount > 0 {
		title += fmt.Sprintf(" ── %d alert", alertCount)
		if alertCount > 1 {
			title += "s"
		}
	}

	// 1/3 metrics, 2/3 logs.
	metricsH := height / 3
	if metricsH < 11 {
		metricsH = 11
	}
	logH := height - metricsH - 1
	if logH < 5 {
		metricsH = height - 6
		logH = 5
	}

	rc := RenderContext{Width: width, Height: metricsH, Theme: theme, WindowLabel: a.windowLabel(), WindowSec: a.windowSeconds()}
	metricsContent := renderDetailGroupMetrics(s, det, rc)
	metricsBox := Box(title, metricsContent, width, metricsH, theme)

	var logBox string
	if det.logs != nil && logH > 3 {
		logBox = renderDetailLogs(det, det.project, true, width, logH, theme, true, a.tsFormat())
	}

	return metricsBox + "\n" + logBox
}

func renderDetailGroupMetrics(s *Session, det *DetailState, rc RenderContext) string {
	theme := rc.Theme
	innerW := rc.Width - 2

	// Aggregate CPU/MEM across all containers in the group.
	var totalCPU float64
	var totalMemUsage, totalMemLimit uint64
	for _, id := range det.projectIDs {
		for _, c := range s.Containers {
			if c.ID == id {
				totalCPU += c.CPUPercent
				totalMemUsage += c.MemUsage
				totalMemLimit += c.MemLimit
				break
			}
		}
	}

	// Per-container table: header + one row per container + blank separator.
	tableLines := 2 + len(det.projectIDs)
	graphBudget := rc.Height - 2 - tableLines
	if graphBudget < 5 {
		graphBudget = 5
	}

	// Side-by-side CPU and MEM inner boxes.
	leftW := innerW / 2
	rightW := innerW - leftW
	graphRows := graphBudget - 2 // inner box borders
	if graphRows < 1 {
		graphRows = 1
	}

	// Auto-scaled for groups (aggregated percentages don't fit 0-100).
	cpuVal := fmt.Sprintf("%5.1f%%", totalCPU)
	cpuAgg := aggregateHistory(s.CPUHistory, det.projectIDs)
	var cpuContent string
	if len(cpuAgg) > 0 {
		cpuContent = strings.Join(autoGridGraph(cpuAgg, cpuVal, leftW-2, graphRows, rc.WindowSec, theme, theme.CPUGraph, pctAxis), "\n")
	} else {
		cpuContent = fmt.Sprintf(" CPU %s", cpuVal)
	}

	memVal := fmt.Sprintf("%s / %s", FormatBytes(totalMemUsage), FormatBytes(totalMemLimit))
	memAgg := aggregateHistory(s.MemHistory, det.projectIDs)
	var memContent string
	if len(memAgg) > 0 {
		memContent = strings.Join(autoGridGraph(memAgg, memVal, rightW-2, graphRows, rc.WindowSec, theme, theme.MemGraph, bytesAxis), "\n")
	} else {
		memContent = fmt.Sprintf(" MEM %s", memVal)
	}

	cpuTitle := "CPU · " + rc.WindowLabel
	memTitle := "Memory · " + rc.WindowLabel
	graphs := lipgloss.JoinHorizontal(lipgloss.Top,
		Box(cpuTitle, cpuContent, leftW, graphBudget, theme),
		Box(memTitle, memContent, rightW, graphBudget, theme))

	var lines []string
	lines = append(lines, strings.Split(graphs, "\n")...)

	lines = append(lines, "")

	// Per-container summary table.
	muted := lipgloss.NewStyle().Foreground(theme.Muted)
	lines = append(lines, muted.Render(" CONTAINER          STATE     CPU     MEM"))
	for _, id := range det.projectIDs {
		name := containerNameByID(id, s.ContInfo)
		if name == "" {
			name = id[:min(12, len(id))]
		}
		var cm *protocol.ContainerMetrics
		for i := range s.Containers {
			if s.Containers[i].ID == id {
				cm = &s.Containers[i]
				break
			}
		}
		if cm != nil {
			indicator := theme.StateIndicator(cm.State)
			line := fmt.Sprintf(" %s %-18s %-8s %5.1f%% %6s",
				indicator, Truncate(name, 18), Truncate(cm.State, 8),
				cm.CPUPercent, FormatBytes(cm.MemUsage))
			lines = append(lines, TruncateStyled(line, innerW))
		} else {
			line := fmt.Sprintf("   %-18s %-8s    —      —", Truncate(name, 18), "—")
			lines = append(lines, muted.Render(Truncate(line, innerW)))
		}
	}

	return strings.Join(lines, "\n")
}

func renderDetailMetrics(s *Session, det *DetailState, cm *protocol.ContainerMetrics, rc RenderContext) string {
	if cm == nil {
		return "  Waiting for metrics..."
	}
	theme := rc.Theme
	innerW := rc.Width - 2

	// Info lines: NET+BLK, PID+HC, IMG+UP = 3 fixed.
	infoLines := 3
	graphBudget := rc.Height - 2 - infoLines
	if graphBudget < 5 {
		graphBudget = 5
	}

	// Side-by-side CPU and MEM inner boxes.
	leftW := innerW / 2
	rightW := innerW - leftW
	graphRows := graphBudget - 2 // inner box borders
	if graphRows < 1 {
		graphRows = 1
	}

	cpuVal := fmt.Sprintf("%5.1f%%", cm.CPUPercent)
	cpuData := historyData(s.CPUHistory, det.containerID)
	var cpuContent string
	if len(cpuData) > 0 {
		cpuContent = strings.Join(autoGridGraph(cpuData, cpuVal, leftW-2, graphRows, rc.WindowSec, theme, theme.CPUGraph, pctAxis), "\n")
	} else {
		cpuContent = fmt.Sprintf(" CPU %s", cpuVal)
	}

	memVal := FormatBytes(cm.MemUsage)
	memData := historyData(s.MemHistory, det.containerID)
	var memContent string
	if len(memData) > 0 {
		memContent = strings.Join(autoGridGraph(memData, memVal, rightW-2, graphRows, rc.WindowSec, theme, theme.MemGraph, bytesAxis), "\n")
	} else {
		memContent = fmt.Sprintf(" MEM %s", memVal)
	}

	cpuTitle := "CPU · " + rc.WindowLabel
	memTitle := "Memory · " + rc.WindowLabel
	graphs := lipgloss.JoinHorizontal(lipgloss.Top,
		Box(cpuTitle, cpuContent, leftW, graphBudget, theme),
		Box(memTitle, memContent, rightW, graphBudget, theme))

	var lines []string
	lines = append(lines, strings.Split(graphs, "\n")...)

	// NET + BLK on one line.
	rates := s.Rates.ContainerRates[det.containerID]
	rxStyle := lipgloss.NewStyle().Foreground(theme.Healthy)
	txStyle := lipgloss.NewStyle().Foreground(theme.Accent)
	lines = append(lines, fmt.Sprintf(" NET  %s %s  %s %s    BLK  %s %s  %s %s",
		rxStyle.Render("▼"), FormatBytesRate(rates.NetRxRate),
		txStyle.Render("▲"), FormatBytesRate(rates.NetTxRate),
		rxStyle.Render("R"), FormatBytesRate(rates.BlockReadRate),
		txStyle.Render("W"), FormatBytesRate(rates.BlockWriteRate)))

	// PID + RESTARTS + HC on one line.
	uptime := formatContainerUptime(cm.State, cm.StartedAt, cm.ExitCode)
	lines = append(lines, fmt.Sprintf(" PID  %d    %s    HC %s",
		cm.PIDs, formatRestarts(cm.RestartCount, theme), theme.HealthText(cm.Health)))

	// IMG + UP on one line.
	var image string
	for _, ci := range s.ContInfo {
		if ci.ID == det.containerID {
			image = stripANSI(ci.Image)
			break
		}
	}
	if image == "" {
		image = stripANSI(cm.Image)
	}
	imgLine := fmt.Sprintf(" IMG  %s    UP %s", Truncate(image, innerW-20), uptime)
	lines = append(lines, Truncate(imgLine, innerW))

	return strings.Join(lines, "\n")
}

func historyData(hist map[string]*RingBuffer[float64], id string) []float64 {
	if buf, ok := hist[id]; ok {
		return buf.Data()
	}
	return nil
}

// aggregateHistory sums per-index values across multiple container histories (right-aligned).
func aggregateHistory(histories map[string]*RingBuffer[float64], ids []string) []float64 {
	maxLen := 0
	for _, id := range ids {
		if buf, ok := histories[id]; ok {
			d := buf.Data()
			if len(d) > maxLen {
				maxLen = len(d)
			}
		}
	}
	if maxLen == 0 {
		return nil
	}

	agg := make([]float64, maxLen)
	for _, id := range ids {
		buf, ok := histories[id]
		if !ok {
			continue
		}
		d := buf.Data()
		offset := maxLen - len(d)
		for i, v := range d {
			agg[offset+i] += v
		}
	}
	return agg
}

// containerAlerts returns active alerts that match a container ID.
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

