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
func renderDetail(a *App, width, height int) string {
	theme := &a.theme
	s := &a.detail

	if s.containerID == "" {
		return Box("Detail", "  No container selected. Press Enter on a container in the dashboard.", width, height, theme)
	}

	// Find current container metrics.
	var cm *protocol.ContainerMetrics
	for i := range a.containers {
		if a.containers[i].ID == s.containerID {
			cm = &a.containers[i]
			break
		}
	}

	// Find container info.
	var ci *protocol.ContainerInfo
	for i := range a.contInfo {
		if a.contInfo[i].ID == s.containerID {
			ci = &a.contInfo[i]
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

	// Top section: metrics. CPU (3 graph) + MEM (3 graph) + NET + BLK + PID + IMG + UP + RESTARTS+HC = 12 content + 2 borders = 14
	metricsH := 14
	logH := height - metricsH - 1
	if logH < 5 {
		metricsH = height - 6
		logH = 5
	}

	metricsContent := renderDetailMetrics(a, s, cm, width, metricsH, theme)
	metricsBox := Box(title, metricsContent, width, metricsH, theme)

	// Bottom section: logs.
	containerName := ""
	if ci != nil {
		containerName = stripANSI(ci.Name)
	} else if cm != nil {
		containerName = stripANSI(cm.Name)
	}
	var logBox string
	if s.logs != nil && logH > 3 {
		logBox = renderDetailLogs(s, containerName, width, logH, theme)
	}

	// Restart confirmation overlay.
	if s.confirmRestart {
		return metricsBox + "\n" + renderRestartConfirm(cm, width, logH, theme)
	}

	return metricsBox + "\n" + logBox
}

func renderDetailMetrics(a *App, s *DetailState, cm *protocol.ContainerMetrics, width, height int, theme *Theme) string {
	if cm == nil {
		return "  Waiting for metrics..."
	}
	innerW := width - 2
	graphRows := 3
	labelW := 5 // " CPU " / " MEM "

	var lines []string

	// CPU + MEM graphs with aligned widths.
	cpuVal := fmt.Sprintf("%5.1f%%", cm.CPUPercent)
	memVal := fmt.Sprintf("%s / %s limit", FormatBytes(cm.MemUsage), FormatBytes(cm.MemLimit))
	valW := max(len(cpuVal), len(memVal))
	graphW := innerW - labelW - valW - 1
	if graphW < 10 {
		graphW = 10
	}
	cpuVal = fmt.Sprintf("%*s", valW, cpuVal)
	memVal = fmt.Sprintf("%*s", valW, memVal)

	cpuData := historyData(a.cpuHistory, s.containerID)
	if len(cpuData) > 0 {
		cpuGraph := Graph(cpuData, graphW, graphRows, 0, theme)
		for i, gl := range strings.Split(cpuGraph, "\n") {
			if i == 0 {
				lines = append(lines, " CPU "+gl+" "+cpuVal)
			} else {
				lines = append(lines, strings.Repeat(" ", labelW)+gl)
			}
		}
	} else {
		lines = append(lines, fmt.Sprintf(" CPU: %s", cpuVal))
	}

	memData := historyData(a.memHistory, s.containerID)
	if len(memData) > 0 {
		memGraph := Graph(memData, graphW, graphRows, 0, theme)
		for i, gl := range strings.Split(memGraph, "\n") {
			if i == 0 {
				lines = append(lines, " MEM "+gl+" "+memVal)
			} else {
				lines = append(lines, strings.Repeat(" ", labelW)+gl)
			}
		}
	} else {
		lines = append(lines, fmt.Sprintf(" MEM: %s", memVal))
	}

	// NET + BLK on separate lines.
	rates := a.rates.ContainerRates[s.containerID]
	rxStyle := lipgloss.NewStyle().Foreground(theme.Healthy)
	txStyle := lipgloss.NewStyle().Foreground(theme.Accent)
	lines = append(lines, fmt.Sprintf(" NET  %s %s  %s %s",
		rxStyle.Render("▼"), FormatBytesRate(rates.NetRxRate),
		txStyle.Render("▲"), FormatBytesRate(rates.NetTxRate)))
	lines = append(lines, fmt.Sprintf(" BLK  %s %s  %s %s",
		rxStyle.Render("R"), FormatBytesRate(rates.BlockReadRate),
		txStyle.Render("W"), FormatBytesRate(rates.BlockWriteRate)))

	// PID, IMG, UP, RESTARTS+HC.
	// Find container info for image.
	var image string
	for _, ci := range a.contInfo {
		if ci.ID == s.containerID {
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
	healthFull := theme.HealthText(cm.Health)
	lines = append(lines, fmt.Sprintf(" RESTARTS  %-6d HC   %s", cm.RestartCount, healthFull))

	return strings.Join(lines, "\n")
}

func historyData(hist map[string]*RingBuffer[float64], id string) []float64 {
	if buf, ok := hist[id]; ok {
		return buf.Data()
	}
	return nil
}

func renderDetailLogs(s *DetailState, containerName string, width, height int, theme *Theme) string {
	hasFilters := s.filterStream != "" || s.searchText != "" || s.searchMode
	boxH := height
	if hasFilters {
		boxH = height - 1 // leave room for filter footer
	}
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

	cursorStyle := lipgloss.NewStyle().Reverse(true)
	var lines []string
	for i, entry := range visible {
		line := formatLogLine(entry, innerW, theme)
		if i == s.logCursor {
			line = cursorStyle.Render(line)
		}
		lines = append(lines, line)
		if i == s.logExpanded {
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
	if hasFilters {
		return box + "\n" + renderDetailLogFooter(s, innerW, theme)
	}
	return box
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

	footer := fmt.Sprintf(" s: %s | %s | Esc clear", muted.Render(streamLabel), searchPart)
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
func updateDetail(a *App, msg tea.KeyMsg) tea.Cmd {
	s := &a.detail
	key := msg.String()

	if s.confirmRestart {
		switch key {
		case "y":
			s.confirmRestart = false
			id := s.containerID
			client := a.client
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				client.RestartContainer(ctx, id)
				return restartDoneMsg{}
			}
		case "n", "esc":
			s.confirmRestart = false
		}
		return nil
	}

	// Search mode captures all keys.
	if s.searchMode {
		switch key {
		case "enter", "esc":
			s.searchMode = false
		case "backspace":
			if len(s.searchText) > 0 {
				s.searchText = s.searchText[:len(s.searchText)-1]
			}
		default:
			if len(key) == 1 && len(s.searchText) < 128 {
				s.searchText += key
			}
		}
		return nil
	}

	data := s.filteredData()
	// Compute innerH for cursor bounds (same formula as renderDetail).
	metricsH := 14
	logH := a.height - 1 - metricsH - 1
	if logH < 5 {
		logH = 5
	}
	innerH := logH - 2
	if innerH < 1 {
		innerH = 1
	}
	// Reserve footer line when filters are active.
	if s.filterStream != "" || s.searchText != "" || s.searchMode {
		innerH--
		if innerH < 1 {
			innerH = 1
		}
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
		s.confirmRestart = true
	case "j", "down":
		if s.logCursor == -1 {
			s.logCursor = visibleCount - 1
			if s.logCursor < 0 {
				s.logCursor = 0
			}
		} else if s.logCursor < visibleCount-1 {
			s.logCursor++
		} else if s.logScroll > 0 {
			s.logScroll--
		}
		s.logExpanded = -1
	case "k", "up":
		if s.logCursor == -1 {
			s.logCursor = visibleCount - 1
			if s.logCursor < 0 {
				s.logCursor = 0
			}
		} else if s.logCursor > 0 {
			s.logCursor--
		} else if s.logScroll < maxScroll {
			s.logScroll++
		}
		s.logExpanded = -1
	case "enter":
		if s.logCursor >= 0 {
			if s.logExpanded == s.logCursor {
				s.logExpanded = -1
			} else {
				s.logExpanded = s.logCursor
			}
		}
	case "/":
		s.searchMode = true
	case "s":
		switch s.filterStream {
		case "":
			s.filterStream = "stdout"
		case "stdout":
			s.filterStream = "stderr"
		default:
			s.filterStream = ""
		}
		s.logScroll = 0
		s.logCursor = -1
		s.logExpanded = -1
	case "esc":
		if s.searchText != "" {
			s.searchText = ""
			s.logScroll = 0
			s.logCursor = -1
			s.logExpanded = -1
		} else if s.filterStream != "" {
			s.filterStream = ""
			s.logScroll = 0
			s.logCursor = -1
			s.logExpanded = -1
		} else if s.logCursor >= 0 {
			s.logCursor = -1
			s.logExpanded = -1
			s.logScroll = 0
		} else {
			a.active = viewDashboard
		}
	}
	return nil
}
