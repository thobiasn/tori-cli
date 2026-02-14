package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/tori-cli/internal/protocol"
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

	// Find current container metrics (needed for graphs and title).
	var cm *protocol.ContainerMetrics
	for i := range s.Containers {
		if s.Containers[i].ID == det.containerID {
			cm = &s.Containers[i]
			break
		}
	}

	// Title for the info box (name + alert count, no state).
	title := "Info"
	for i := range s.ContInfo {
		if s.ContInfo[i].ID == det.containerID {
			title = stripANSI(s.ContInfo[i].Name)
			break
		}
	}
	if title == "Info" && cm != nil {
		title = stripANSI(cm.Name)
	}
	alertCount := len(containerAlerts(s.Alerts, det.containerID))
	if alertCount > 0 {
		title += fmt.Sprintf(" ── %d alert", alertCount)
		if alertCount > 1 {
			title += "s"
		}
	}

	// 1/3 top row, 2/3 logs.
	metricsH := height / 4
	if metricsH < 11 {
		metricsH = 11
	}
	logH := height - metricsH
	if logH < 5 {
		metricsH = height - 5
		logH = 5
	}

	// Left: info box. Right: graph boxes (no outer wrapper).
	infoW := width * 22 / 100
	if infoW < 28 {
		infoW = 28
	}
	graphW := width - infoW

	infoBox := renderDetailInfoBox(s, det, title, RenderContext{Width: infoW, Height: metricsH, Theme: theme, SpinnerFrame: a.spinnerFrame})

	rc := RenderContext{Width: graphW, Height: metricsH, Theme: theme, WindowLabel: a.windowLabel(), WindowSec: a.windowSeconds(), SpinnerFrame: a.spinnerFrame}
	graphs := renderDetailMetrics(s, det, cm, rc)

	topRow := lipgloss.JoinHorizontal(lipgloss.Top, infoBox, graphs)

	// Inline alerts section (non-focusable).
	var alertBox string
	if ca := containerAlerts(s.Alerts, det.containerID); len(ca) > 0 {
		alertBox = renderDetailAlerts(ca, width, height/6, theme)
		logH -= countLines(alertBox)
		if logH < 3 {
			logH = 3
		}
	}

	// Bottom section: logs.
	containerName := containerNameByID(det.containerID, s.ContInfo)
	if containerName == "" && cm != nil {
		containerName = stripANSI(cm.Name)
	}
	var logBox string
	if det.logs != nil && logH > 3 {
		logBox = renderDetailLogs(det, RenderContext{Width: width, Height: logH, Theme: theme}, detailLogsOpts{
			label: containerName, focused: true, tsFormat: a.tsFormat(),
		})
	}

	result := topRow
	if alertBox != "" {
		result += "\n" + alertBox
	}
	return result + "\n" + logBox
}

func renderDetailGroup(a *App, s *Session, width, height int) string {
	theme := &a.theme
	det := &s.Detail

	// Build title for the containers box.
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

	// 1/3 top row, 2/3 logs.
	metricsH := height / 4
	if metricsH < 11 {
		metricsH = 11
	}
	logH := height - metricsH
	if logH < 5 {
		metricsH = height - 5
		logH = 5
	}

	// Left: containers table. Right: graph boxes (no outer wrapper).
	tableW := width * 26 / 100
	if tableW < 30 {
		tableW = 30
	}
	graphW := width - tableW

	tableBox := renderDetailContainersBox(s, det, title, tableW, metricsH, theme)

	rc := RenderContext{Width: graphW, Height: metricsH, Theme: theme, WindowLabel: a.windowLabel(), WindowSec: a.windowSeconds(), SpinnerFrame: a.spinnerFrame}
	graphs := renderDetailGroupMetrics(s, det, rc)

	topRow := lipgloss.JoinHorizontal(lipgloss.Top, tableBox, graphs)

	// Inline alerts section (non-focusable).
	var alertBox string
	var groupAlerts []*protocol.AlertEvent
	for _, id := range det.projectIDs {
		groupAlerts = append(groupAlerts, containerAlerts(s.Alerts, id)...)
	}
	if len(groupAlerts) > 0 {
		alertBox = renderDetailAlerts(groupAlerts, width, height/6, theme)
		logH -= countLines(alertBox)
		if logH < 3 {
			logH = 3
		}
	}

	var logBox string
	if det.logs != nil && logH > 3 {
		logBox = renderDetailLogs(det, RenderContext{Width: width, Height: logH, Theme: theme}, detailLogsOpts{
			label: det.project, showNames: true, focused: true, tsFormat: a.tsFormat(),
		})
	}

	result := topRow
	if alertBox != "" {
		result += "\n" + alertBox
	}
	return result + "\n" + logBox
}

func renderDetailGroupMetrics(s *Session, det *DetailState, rc RenderContext) string {
	theme := rc.Theme

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

	graphBudget := rc.Height
	if graphBudget < 5 {
		graphBudget = 5
	}

	leftW := rc.Width * 2 / 3
	rightW := rc.Width - leftW
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

	return graphs
}

func renderDetailMetrics(s *Session, det *DetailState, cm *protocol.ContainerMetrics, rc RenderContext) string {
	if cm == nil {
		return lipgloss.Place(rc.Width, rc.Height, lipgloss.Center, lipgloss.Center,
			SpinnerView(rc.SpinnerFrame, "Waiting for metrics...", rc.Theme))
	}
	theme := rc.Theme

	graphBudget := rc.Height
	if graphBudget < 5 {
		graphBudget = 5
	}

	leftW := rc.Width * 2 / 3
	rightW := rc.Width - leftW
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

	return graphs
}

// renderDetailInfoBox renders the container info panel for single-container detail.
func renderDetailInfoBox(s *Session, det *DetailState, title string, rc RenderContext) string {
	theme := rc.Theme

	// Look up container metrics and info.
	var cm *protocol.ContainerMetrics
	for i := range s.Containers {
		if s.Containers[i].ID == det.containerID {
			cm = &s.Containers[i]
			break
		}
	}
	var ci *protocol.ContainerInfo
	for i := range s.ContInfo {
		if s.ContInfo[i].ID == det.containerID {
			ci = &s.ContInfo[i]
			break
		}
	}

	if cm == nil {
		return Box(title, SpinnerViewCentered(rc.SpinnerFrame, "Waiting...", theme, rc.Width-2, rc.Height-2), rc.Width, rc.Height, theme)
	}
	innerW := rc.Width - 2

	var image string
	if ci != nil {
		image = stripANSI(ci.Image)
	}
	if image == "" {
		image = stripANSI(cm.Image)
	}

	uptime := formatContainerUptime(cm.State, cm.StartedAt, cm.ExitCode)
	stateInd := theme.StateIndicator(cm.State)
	status := stateInd + " " + stripANSI(cm.State)
	if uptime != "" {
		status += " · " + uptime
	}

	health := theme.HealthText(cm.Health)
	restarts := formatRestarts(cm.RestartCount, theme)

	rates := s.Rates.ContainerRates[det.containerID]
	rxStyle := lipgloss.NewStyle().Foreground(theme.Healthy)
	txStyle := lipgloss.NewStyle().Foreground(theme.Accent)

	valCol := innerW - 10 // label column ~9 chars + leading space

	var lines []string
	lines = append(lines, fmt.Sprintf(" %-8s %s", "Image", Truncate(image, valCol)))
	lines = append(lines, fmt.Sprintf(" %-8s %s", "Status", TruncateStyled(status, valCol)))
	lines = append(lines, fmt.Sprintf(" %-8s %d", "PID", cm.PIDs))
	lines = append(lines, fmt.Sprintf(" %-8s %s  %s", "Health", health, restarts))
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf(" Net %s  %-10s Net %s  %s",
		rxStyle.Render("▼"), FormatBytesRate(rates.NetRxRate),
		txStyle.Render("▲"), FormatBytesRate(rates.NetTxRate)))
	lines = append(lines, fmt.Sprintf(" Blk %s  %-10s Blk %s  %s",
		rxStyle.Render("R"), FormatBytesRate(rates.BlockReadRate),
		txStyle.Render("W"), FormatBytesRate(rates.BlockWriteRate)))

	return Box(title, strings.Join(lines, "\n"), rc.Width, rc.Height, theme)
}

// renderDetailContainersBox renders the per-container table for group detail.
func renderDetailContainersBox(s *Session, det *DetailState, title string, width, height int, theme *Theme) string {
	innerW := width - 2
	muted := lipgloss.NewStyle().Foreground(theme.Muted)

	// Fixed columns: ● (2) + STATUS(7) + CPU(5) + MEM(6) + UP(3) + H(1) + spacing(12) = 36
	fixedW := 36
	nameW := innerW - fixedW - 1
	if nameW < 6 {
		nameW = 6
	}

	var lines []string
	header := fmt.Sprintf(" %-*s   %-7s  %5s  %6s  %3s  %s", nameW, "NAME", "STATUS", "CPU", "MEM", "UP", "H")
	lines = append(lines, muted.Render(Truncate(header, innerW)))

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
			health := theme.HealthIndicator(cm.Health)
			up := compactUptime(cm.State, cm.StartedAt)
			line := fmt.Sprintf(" %s %-*s   %-7s  %4.1f%%  %6s  %3s  %s",
				indicator, nameW-2, Truncate(name, nameW-2),
				Truncate(cm.State, 7),
				cm.CPUPercent, FormatBytes(cm.MemUsage),
				up, health)
			lines = append(lines, TruncateStyled(line, innerW))
		} else {
			line := fmt.Sprintf("   %-*s   %-7s  %5s  %6s  %3s  %s",
				nameW-2, Truncate(name, nameW-2), "—", "—", "—", "—", "–")
			lines = append(lines, muted.Render(Truncate(line, innerW)))
		}
	}

	return Box(title, strings.Join(lines, "\n"), width, height, theme)
}

// compactUptime returns a short uptime string like "4d", "2h", "5m".
func compactUptime(state string, startedAt int64) string {
	if state != "running" || startedAt <= 0 {
		return "—"
	}
	secs := time.Now().Unix() - startedAt
	if secs < 0 {
		secs = 0
	}
	days := int(secs / 86400)
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	hours := int(secs / 3600)
	if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}
	mins := int(secs / 60)
	if mins > 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return "<1m"
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

// renderDetailAlerts renders a compact, non-focusable alerts section.
func renderDetailAlerts(alerts []*protocol.AlertEvent, width, maxH int, theme *Theme) string {
	innerW := width - 2
	var lines []string
	for _, a := range alerts {
		sev := severityTag(a.Severity, theme)
		msg := Truncate(a.Message, innerW-25)
		line := fmt.Sprintf(" %s  %-16s %s", sev, Truncate(a.RuleName, 16), msg)
		lines = append(lines, Truncate(line, innerW))
	}
	h := len(lines) + 2
	if maxH > 2 && h > maxH {
		h = maxH
		lines = lines[:maxH-2]
	}
	return Box("Alerts", strings.Join(lines, "\n"), width, h, theme)
}

// countLines returns the number of visual lines in a rendered string.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

