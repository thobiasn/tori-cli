package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/rook/internal/protocol"
)

// DetailState holds state for the container/group detail view.
type DetailState struct {
	containerID string // single-container mode (empty in group mode)
	project     string // group mode (empty in single-container mode)
	projectIDs  []string // container IDs in the project (group mode)

	logs      *RingBuffer[protocol.LogEntryMsg]
	logScroll int
	logLive   bool
	logCursor   int // -1 = inactive
	logExpanded int // -1 = none

	// Filters.
	filterContainerID string // within-group container filter (c key)
	filterProject     string // project filter (g key, only meaningful in group mode)
	filterStream      string // "", "stdout", "stderr"
	searchText        string
	searchMode        bool

	backfilled bool
}

type detailLogQueryMsg struct {
	entries []protocol.LogEntryMsg
}

func (s *DetailState) reset() {
	s.logs = NewRingBuffer[protocol.LogEntryMsg](5000)
	s.logScroll = 0
	s.logLive = true
	s.logCursor = -1
	s.logExpanded = -1
	s.filterContainerID = ""
	s.filterProject = ""
	s.filterStream = ""
	s.searchText = ""
	s.searchMode = false
	s.backfilled = false
}

func (s *DetailState) isGroupMode() bool {
	return s.project != "" && s.containerID == ""
}

func (s *DetailState) onSwitch(c *Client) tea.Cmd {
	if s.containerID == "" && s.project == "" {
		return nil
	}
	if s.logs == nil {
		s.reset()
	}
	if s.backfilled {
		return nil
	}
	id := s.containerID
	ids := s.projectIDs
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req := &protocol.QueryLogsReq{
			Start: 0,
			End:   time.Now().Unix(),
			Limit: 500,
		}
		if len(ids) > 0 {
			req.ContainerIDs = ids
		} else if id != "" {
			req.ContainerID = id
		}
		entries, err := c.QueryLogs(ctx, req)
		if err != nil {
			return detailLogQueryMsg{}
		}
		return detailLogQueryMsg{entries: entries}
	}
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
	if len(msg.entries) == 0 {
		s.backfilled = true
		return
	}

	existing := s.logs.Data()
	var oldestTS int64
	if len(existing) > 0 {
		oldestTS = existing[0].Timestamp
	}

	newBuf := NewRingBuffer[protocol.LogEntryMsg](5000)
	for _, e := range msg.entries {
		if oldestTS == 0 || e.Timestamp < oldestTS {
			newBuf.Push(e)
		}
	}
	for _, e := range existing {
		newBuf.Push(e)
	}
	s.logs = newBuf
	s.backfilled = true
}

func (s *DetailState) matchesFilter(entry protocol.LogEntryMsg) bool {
	if s.filterContainerID != "" && entry.ContainerID != s.filterContainerID {
		return false
	}
	if s.filterStream != "" && entry.Stream != s.filterStream {
		return false
	}
	if s.searchText != "" && !strings.Contains(strings.ToLower(entry.Message), strings.ToLower(s.searchText)) {
		return false
	}
	return true
}

func (s *DetailState) filteredData() []protocol.LogEntryMsg {
	if s.logs == nil {
		return nil
	}
	all := s.logs.Data()
	if s.filterContainerID == "" && s.filterStream == "" && s.searchText == "" {
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

func (s *DetailState) cycleContainerFilter(contInfo []protocol.ContainerInfo) {
	// In single-container mode, no container cycling.
	if !s.isGroupMode() {
		return
	}
	ids := s.projectIDs
	if len(ids) == 0 {
		return
	}
	if s.filterContainerID == "" {
		s.filterContainerID = ids[0]
		return
	}
	for i, id := range ids {
		if id == s.filterContainerID {
			if i+1 < len(ids) {
				s.filterContainerID = ids[i+1]
			} else {
				s.filterContainerID = ""
			}
			return
		}
	}
	s.filterContainerID = ""
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

	metricsContent := renderDetailMetrics(s, det, cm, width, metricsH, theme, a.windowLabel(), a.windowSeconds())
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
		logBox = renderDetailLogs(det, containerName, width, logH, theme)
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

	metricsContent := renderDetailGroupMetrics(s, det, width, metricsH, theme, a.windowLabel(), a.windowSeconds())
	metricsBox := Box(title, metricsContent, width, metricsH, theme)

	var logBox string
	if det.logs != nil && logH > 3 {
		logBox = renderDetailLogs(det, det.project, width, logH, theme)
	}

	return metricsBox + "\n" + logBox
}

func renderDetailGroupMetrics(s *Session, det *DetailState, width, height int, theme *Theme, windowLabel string, windowSec int64) string {
	innerW := width - 2

	// Aggregate CPU/MEM/Disk across all containers in the group.
	var totalCPU float64
	var totalMemUsage, totalMemLimit, totalDisk uint64
	for _, id := range det.projectIDs {
		for _, c := range s.Containers {
			if c.ID == id {
				totalCPU += c.CPUPercent
				totalMemUsage += c.MemUsage
				totalMemLimit += c.MemLimit
				totalDisk += c.DiskUsage
				break
			}
		}
	}

	// Per-container table: header + one row per container + blank separator.
	tableLines := 2 + len(det.projectIDs)
	hasDisk := totalDisk > 0
	diskH := 0
	if hasDisk {
		diskH = 3
	}
	graphBudget := height - 2 - tableLines - diskH
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
		cpuContent = strings.Join(autoGridGraph(cpuAgg, cpuVal, leftW-2, graphRows, windowSec, theme, theme.CPUGraph, pctAxis), "\n")
	} else {
		cpuContent = fmt.Sprintf(" CPU %s", cpuVal)
	}

	memVal := fmt.Sprintf("%s / %s", FormatBytes(totalMemUsage), FormatBytes(totalMemLimit))
	memAgg := aggregateHistory(s.MemHistory, det.projectIDs)
	var memContent string
	if len(memAgg) > 0 {
		memContent = strings.Join(autoGridGraph(memAgg, memVal, rightW-2, graphRows, windowSec, theme, theme.MemGraph, bytesAxis), "\n")
	} else {
		memContent = fmt.Sprintf(" MEM %s", memVal)
	}

	cpuTitle := "CPU · " + windowLabel
	memTitle := "Memory · " + windowLabel
	graphs := lipgloss.JoinHorizontal(lipgloss.Top,
		Box(cpuTitle, cpuContent, leftW, graphBudget, theme),
		Box(memTitle, memContent, rightW, graphBudget, theme))

	var lines []string
	lines = append(lines, strings.Split(graphs, "\n")...)

	if hasDisk {
		lines = append(lines, strings.Split(renderGroupDiskBox(totalDisk, innerW, diskH, theme), "\n")...)
	}

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

func renderDetailMetrics(s *Session, det *DetailState, cm *protocol.ContainerMetrics, width, height int, theme *Theme, windowLabel string, windowSec int64) string {
	if cm == nil {
		return "  Waiting for metrics..."
	}
	innerW := width - 2

	// Info lines: NET+BLK, PID+HC, IMG+UP = 3 fixed.
	infoLines := 3
	hasDisk := cm.DiskUsage > 0
	diskH := 0
	if hasDisk {
		diskH = 3
	}
	graphBudget := height - 2 - infoLines - diskH
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
		cpuContent = strings.Join(autoGridGraph(cpuData, cpuVal, leftW-2, graphRows, windowSec, theme, theme.CPUGraph, pctAxis), "\n")
	} else {
		cpuContent = fmt.Sprintf(" CPU %s", cpuVal)
	}

	memVal := FormatBytes(cm.MemUsage)
	memData := historyData(s.MemHistory, det.containerID)
	var memContent string
	if len(memData) > 0 {
		memContent = strings.Join(autoGridGraph(memData, memVal, rightW-2, graphRows, windowSec, theme, theme.MemGraph, bytesAxis), "\n")
	} else {
		memContent = fmt.Sprintf(" MEM %s", memVal)
	}

	cpuTitle := "CPU · " + windowLabel
	memTitle := "Memory · " + windowLabel
	graphs := lipgloss.JoinHorizontal(lipgloss.Top,
		Box(cpuTitle, cpuContent, leftW, graphBudget, theme),
		Box(memTitle, memContent, rightW, graphBudget, theme))

	var lines []string
	lines = append(lines, strings.Split(graphs, "\n")...)

	if hasDisk {
		lines = append(lines, strings.Split(renderContainerDiskBox(cm.DiskUsage, innerW, diskH, theme), "\n")...)
	}

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

func renderDetailLogs(s *DetailState, label string, width, height int, theme *Theme) string {
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
	} else if s.logScroll == 0 && s.logCursor == -1 {
		visible = data[len(data)-innerH:]
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

	// Calculate expansion lines so we can reduce visible entries if needed.
	cursorIdx := s.logCursor
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
		line := formatLogLine(entry, innerW, theme)
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
	paused := s.logScroll > 0 || s.logCursor >= 0
	if paused {
		title += " ── PAUSED"
	} else {
		title += " ── LIVE"
	}

	box := Box(title, strings.Join(lines, "\n"), width, boxH, theme)
	return box + "\n" + renderDetailLogFooter(s, innerW, theme)
}

func renderDetailLogFooter(s *DetailState, width int, theme *Theme) string {
	muted := lipgloss.NewStyle().Foreground(theme.Muted)

	var parts []string

	if s.isGroupMode() {
		contLabel := "all"
		if s.filterContainerID != "" {
			contLabel = Truncate(s.filterContainerID[:min(12, len(s.filterContainerID))], 12)
		}
		parts = append(parts, "c: "+muted.Render(contLabel))
	}

	streamLabel := "all"
	if s.filterStream != "" {
		streamLabel = s.filterStream
	}
	parts = append(parts, "s: "+muted.Render(streamLabel))

	var searchPart string
	if s.searchMode {
		searchPart = fmt.Sprintf("/ search: %s_", s.searchText)
	} else if s.searchText != "" {
		searchPart = fmt.Sprintf("/ search: %s", s.searchText)
	} else {
		searchPart = "/ search"
	}
	parts = append(parts, searchPart)

	parts = append(parts, "Esc clear")

	footer := " " + strings.Join(parts, " | ")
	return Truncate(footer, width)
}

// updateDetail handles keys in the detail view.
func updateDetail(a *App, s *Session, msg tea.KeyMsg) tea.Cmd {
	det := &s.Detail
	key := msg.String()

	// Search mode captures all keys.
	if det.searchMode {
		switch key {
		case "enter", "esc":
			det.searchMode = false
		case "backspace":
			if len(det.searchText) > 0 {
				det.searchText = det.searchText[:len(det.searchText)-1]
			}
		default:
			if len(key) == 1 && len(det.searchText) < 128 {
				det.searchText += key
			}
		}
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
	case "c":
		det.cycleContainerFilter(s.ContInfo)
		det.logScroll = 0
		det.logCursor = -1
		det.logExpanded = -1
	case "g":
		det.cycleProjectFilter(s.ContInfo)
		det.logScroll = 0
		det.logCursor = -1
		det.logExpanded = -1
	case "j", "down":
		if det.logCursor == -1 {
			det.logCursor = visibleCount - 1
			if det.logCursor < 0 {
				det.logCursor = 0
			}
		} else if det.logCursor < visibleCount-1 {
			det.logCursor++
		} else if det.logScroll > 0 {
			det.logScroll--
		}
		det.logExpanded = -1
	case "k", "up":
		if det.logCursor == -1 {
			det.logCursor = visibleCount - 1
			if det.logCursor < 0 {
				det.logCursor = 0
			}
		} else if det.logCursor > 0 {
			det.logCursor--
		} else if det.logScroll < maxScroll {
			det.logScroll++
		}
		det.logExpanded = -1
	case "enter":
		if det.logCursor >= 0 {
			if det.logExpanded == det.logCursor {
				det.logExpanded = -1
			} else {
				det.logExpanded = det.logCursor
			}
		}
	case "/":
		det.searchMode = true
	case "s":
		switch det.filterStream {
		case "":
			det.filterStream = "stdout"
		case "stdout":
			det.filterStream = "stderr"
		default:
			det.filterStream = ""
		}
		det.logScroll = 0
		det.logCursor = -1
		det.logExpanded = -1
	case "esc":
		if det.searchText != "" {
			det.searchText = ""
			det.logScroll = 0
			det.logCursor = -1
			det.logExpanded = -1
		} else if det.filterStream != "" || det.filterContainerID != "" {
			det.filterStream = ""
			det.filterContainerID = ""
			det.logScroll = 0
			det.logCursor = -1
			det.logExpanded = -1
		} else if det.logCursor >= 0 {
			det.logCursor = -1
			det.logExpanded = -1
			det.logScroll = 0
		} else {
			a.active = viewDashboard
		}
	}
	return nil
}
