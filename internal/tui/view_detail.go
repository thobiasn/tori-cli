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

// DetailState holds state for the container detail view.
type DetailState struct {
	containerID    string
	logs           *RingBuffer[protocol.LogEntryMsg]
	logScroll      int
	logLive        bool
	logCursor      int // -1 = inactive
	logExpanded    int // -1 = none
	filterStream   string // "", "stdout", "stderr"
	searchText     string
	searchMode     bool
	confirmRestart bool
	backfilled     bool
}

type detailLogQueryMsg struct {
	entries []protocol.LogEntryMsg
}

func (s *DetailState) reset() {
	s.logs = NewRingBuffer[protocol.LogEntryMsg](2000)
	s.logScroll = 0
	s.logLive = true
	s.logCursor = -1
	s.logExpanded = -1
	s.filterStream = ""
	s.searchText = ""
	s.searchMode = false
	s.confirmRestart = false
	s.backfilled = false
}

func (s *DetailState) onSwitch(c *Client) tea.Cmd {
	if s.containerID == "" {
		return nil
	}
	if s.logs == nil {
		s.reset()
	}
	if s.backfilled {
		return nil
	}
	id := s.containerID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		entries, err := c.QueryLogs(ctx, &protocol.QueryLogsReq{
			Start:       0,
			End:         time.Now().Unix(),
			ContainerID: id,
			Limit:       200,
		})
		if err != nil {
			return detailLogQueryMsg{}
		}
		return detailLogQueryMsg{entries: entries}
	}
}

func (s *DetailState) onStreamEntry(entry protocol.LogEntryMsg) {
	if s.logs == nil || entry.ContainerID != s.containerID {
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

	newBuf := NewRingBuffer[protocol.LogEntryMsg](2000)
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
	if s.filterStream == "" && s.searchText == "" {
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

// renderDetail renders the container detail full-screen view.
func renderDetail(a *App, s *Session, width, height int) string {
	theme := &a.theme
	det := &s.Detail

	if det.containerID == "" {
		return Box("Detail", "  No container selected. Press Enter on a container in the dashboard.", width, height, theme)
	}

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

	// Title.
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

	// Top section: CPU (3) + MEM (3) + NET + BLK + PID + IMG + UP + blank + RESTARTS + HC = 15 content + 2 borders = 17
	metricsH := 17
	logH := height - metricsH - 1
	if logH < 5 {
		metricsH = height - 6
		logH = 5
	}

	// Split top section: left = metrics, right = alerts (50/50).
	leftW := width / 2
	rightW := width - leftW

	metricsContent := renderDetailMetrics(s, det, cm, leftW, metricsH, theme)
	metricsBox := Box(title, metricsContent, leftW, metricsH, theme)

	alertsContent := renderDetailAlerts(s, det.containerID, rightW, metricsH, theme)
	alertsBox := Box("Alerts", alertsContent, rightW, metricsH, theme)

	topRow := lipgloss.JoinHorizontal(lipgloss.Top, metricsBox, alertsBox)

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

	// Restart confirmation overlay.
	if det.confirmRestart {
		return topRow + "\n" + renderRestartConfirm(cm, width, logH, theme)
	}

	return topRow + "\n" + logBox
}

func renderDetailMetrics(s *Session, det *DetailState, cm *protocol.ContainerMetrics, width, height int, theme *Theme) string {
	if cm == nil {
		return "  Waiting for metrics..."
	}
	innerW := width - 2
	graphRows := 3
	labelW := 5 // " CPU " / " MEM "

	var lines []string

	// CPU + MEM: value next to label (right-aligned), graph fills remaining width.
	cpuVal := fmt.Sprintf("%5.1f%%", cm.CPUPercent)
	memVal := fmt.Sprintf("%s / %s limit", FormatBytes(cm.MemUsage), FormatBytes(cm.MemLimit))
	valW := max(len(cpuVal), len(memVal))
	leftW := labelW + valW + 1 // " CPU " + padded value + space before graph
	graphW := innerW - leftW
	if graphW < 10 {
		graphW = 10
	}
	cpuVal = fmt.Sprintf("%*s", valW, cpuVal)
	memVal = fmt.Sprintf("%*s", valW, memVal)
	graphPad := strings.Repeat(" ", leftW)

	cpuData := historyData(s.CPUHistory, det.containerID)
	if len(cpuData) > 0 {
		cpuGraph := Graph(cpuData, graphW, graphRows, 0, theme)
		for i, gl := range strings.Split(cpuGraph, "\n") {
			if i == 0 {
				lines = append(lines, " CPU "+cpuVal+" "+gl)
			} else {
				lines = append(lines, graphPad+gl)
			}
		}
	} else {
		lines = append(lines, fmt.Sprintf(" CPU %s", cpuVal))
	}

	memData := historyData(s.MemHistory, det.containerID)
	if len(memData) > 0 {
		memGraph := Graph(memData, graphW, graphRows, 0, theme)
		for i, gl := range strings.Split(memGraph, "\n") {
			if i == 0 {
				lines = append(lines, " MEM "+memVal+" "+gl)
			} else {
				lines = append(lines, graphPad+gl)
			}
		}
	} else {
		lines = append(lines, fmt.Sprintf(" MEM %s", memVal))
	}

	// NET + BLK on separate lines.
	rates := s.Rates.ContainerRates[det.containerID]
	rxStyle := lipgloss.NewStyle().Foreground(theme.Healthy)
	txStyle := lipgloss.NewStyle().Foreground(theme.Accent)
	lines = append(lines, fmt.Sprintf(" NET  %s %s  %s %s",
		rxStyle.Render("▼"), FormatBytesRate(rates.NetRxRate),
		txStyle.Render("▲"), FormatBytesRate(rates.NetTxRate)))
	lines = append(lines, fmt.Sprintf(" BLK  %s %s  %s %s",
		rxStyle.Render("R"), FormatBytesRate(rates.BlockReadRate),
		txStyle.Render("W"), FormatBytesRate(rates.BlockWriteRate)))

	// PID, IMG, UP.
	var image string
	for _, ci := range s.ContInfo {
		if ci.ID == det.containerID {
			image = stripANSI(ci.Image)
			break
		}
	}
	lines = append(lines, fmt.Sprintf(" PID  %d", cm.PIDs))
	if image != "" {
		lines = append(lines, fmt.Sprintf(" IMG  %s", Truncate(image, innerW-6)))
	} else {
		lines = append(lines, fmt.Sprintf(" IMG  %s", Truncate(stripANSI(cm.Image), innerW-6)))
	}
	uptime := formatContainerUptime(cm.State, cm.StartedAt, cm.ExitCode)
	lines = append(lines, fmt.Sprintf(" UP   %s", uptime))

	// Blank line, then HC + RESTARTS grouped.
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf(" HC   %s", theme.HealthText(cm.Health)))
	lines = append(lines, fmt.Sprintf(" RESTARTS  %s", formatRestarts(cm.RestartCount, theme)))

	return strings.Join(lines, "\n")
}

func historyData(hist map[string]*RingBuffer[float64], id string) []float64 {
	if buf, ok := hist[id]; ok {
		return buf.Data()
	}
	return nil
}

// containerAlerts returns active alerts that match a container ID.
// Instance keys use the format "rulename:containerID".
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

func renderDetailAlerts(s *Session, containerID string, width, height int, theme *Theme) string {
	alerts := containerAlerts(s.Alerts, containerID)
	if len(alerts) == 0 {
		return lipgloss.NewStyle().Foreground(theme.Muted).Render("  No active alerts")
	}

	innerW := width - 2
	muted := lipgloss.NewStyle().Foreground(theme.Muted)
	var lines []string
	for _, alert := range alerts {
		sev := severityTag(alert.Severity, theme)
		ts := time.Unix(alert.FiredAt, 0).Format("15:04")
		rule := Truncate(alert.RuleName, 16)
		line := fmt.Sprintf(" %s %s %s", sev, ts, rule)
		lines = append(lines, TruncateStyled(line, innerW))
		if alert.Message != "" {
			lines = append(lines, "  "+Truncate(alert.Message, innerW-3))
		}
		lines = append(lines, muted.Render("  "+alert.Condition))
	}
	return strings.Join(lines, "\n")
}

func renderDetailLogs(s *DetailState, containerName string, width, height int, theme *Theme) string {
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
	if containerName != "" {
		title += " ── " + containerName
	}
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

	streamLabel := "all"
	if s.filterStream != "" {
		streamLabel = s.filterStream
	}

	var searchPart string
	if s.searchMode {
		searchPart = fmt.Sprintf("/ search: %s_", s.searchText)
	} else if s.searchText != "" {
		searchPart = fmt.Sprintf("/ search: %s", s.searchText)
	} else {
		searchPart = "/ search"
	}

	footer := fmt.Sprintf(" s: %s | %s | r restart | Esc clear", muted.Render(streamLabel), searchPart)
	return Truncate(footer, width)
}

func renderRestartConfirm(cm *protocol.ContainerMetrics, width, height int, theme *Theme) string {
	name := ""
	if cm != nil {
		name = cm.Name
	}
	content := fmt.Sprintf("\n  Restart container %s?\n\n  Press y to confirm, n to cancel", name)
	return Box("Confirm Restart", content, width, height, theme)
}

type restartDoneMsg struct{}

// updateDetail handles keys in the detail view.
func updateDetail(a *App, s *Session, msg tea.KeyMsg) tea.Cmd {
	det := &s.Detail
	key := msg.String()

	if det.confirmRestart {
		switch key {
		case "y":
			det.confirmRestart = false
			id := det.containerID
			client := s.Client
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				client.RestartContainer(ctx, id)
				return restartDoneMsg{}
			}
		case "n", "esc":
			det.confirmRestart = false
		}
		return nil
	}

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
	metricsH := 17
	logH := a.height - 1 - metricsH - 1
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
	case "r":
		det.confirmRestart = true
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
		} else if det.filterStream != "" {
			det.filterStream = ""
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
