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
			title += " -- " + stripANSI(cm.State)
		}
		title += " -- " + stripANSI(ci.Image)
	} else if cm != nil {
		title = stripANSI(cm.Name) + " -- " + stripANSI(cm.State)
	}

	// Top section: metrics. Enough room for graph rows + info lines.
	metricsH := 14
	logH := height - metricsH - 1
	if logH < 5 {
		metricsH = height - 6
		logH = 5
	}

	metricsContent := renderDetailMetrics(a, s, cm, width, metricsH, theme)
	metricsBox := Box(title, metricsContent, width, metricsH, theme)

	// Bottom section: logs.
	var logBox string
	if s.logs != nil && logH > 3 {
		logBox = renderDetailLogs(s, width, logH, theme)
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
	graphRows := 2
	labelW := 5 // " CPU " / " MEM "

	var lines []string

	// CPU graph + percent.
	cpuVal := fmt.Sprintf("%5.1f%%", cm.CPUPercent)
	graphW := innerW - labelW - len(cpuVal) - 1
	if graphW < 10 {
		graphW = 10
	}
	cpuData := historyData(a.cpuHistory, s.containerID)
	if len(cpuData) > 0 {
		cpuGraph := Graph(cpuData, graphW, graphRows, 100, theme)
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

	// MEM graph + usage.
	memVal := fmt.Sprintf("%s / %s", FormatBytes(cm.MemUsage), FormatBytes(cm.MemLimit))
	memGraphW := innerW - labelW - len(memVal) - 1
	if memGraphW < 10 {
		memGraphW = 10
	}
	memData := historyData(a.memHistory, s.containerID)
	if len(memData) > 0 {
		memGraph := Graph(memData, memGraphW, graphRows, 100, theme)
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

	// NET rates.
	rates := a.rates.ContainerRates[s.containerID]
	rxStyle := lipgloss.NewStyle().Foreground(theme.Healthy)
	txStyle := lipgloss.NewStyle().Foreground(theme.Accent)
	netLine := fmt.Sprintf(" NET %s %s  %s %s",
		rxStyle.Render("▼"), FormatBytesRate(rates.NetRxRate),
		txStyle.Render("▲"), FormatBytesRate(rates.NetTxRate))
	lines = append(lines, netLine)

	// BLK rates.
	blkLine := fmt.Sprintf(" BLK %s %s  %s %s",
		rxStyle.Render("R"), FormatBytesRate(rates.BlockReadRate),
		txStyle.Render("W"), FormatBytesRate(rates.BlockWriteRate))
	lines = append(lines, blkLine)

	// PIDs + health + restarts.
	healthStr := theme.HealthIndicator(cm.Health)
	uptime := formatContainerUptime(cm.State, cm.StartedAt, cm.ExitCode)
	lines = append(lines, fmt.Sprintf(" PIDs: %d  %s  %s  %s", cm.PIDs, healthStr, uptime, formatRestarts(cm.RestartCount, theme)))

	return strings.Join(lines, "\n")
}

func historyData(hist map[string]*RingBuffer[float64], id string) []float64 {
	if buf, ok := hist[id]; ok {
		return buf.Data()
	}
	return nil
}

func renderDetailLogs(s *DetailState, width, height int, theme *Theme) string {
	innerH := height - 2
	if innerH < 1 {
		innerH = 1
	}
	innerW := width - 2

	data := s.logs.Data()
	// Apply scroll.
	var visible []protocol.LogEntryMsg
	if len(data) <= innerH {
		visible = data
	} else if s.logScroll == 0 {
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

	var lines []string
	for _, entry := range visible {
		lines = append(lines, formatLogLine(entry, innerW, theme))
	}

	title := "Logs"
	if s.logScroll == 0 {
		title += " -- LIVE"
	} else {
		title += " -- PAUSED"
	}
	return Box(title, strings.Join(lines, "\n"), width, height, theme)
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

	maxScroll := s.logs.Len() - 1
	if maxScroll < 0 {
		maxScroll = 0
	}

	switch key {
	case "r":
		s.confirmRestart = true
	case "j", "down":
		s.logScroll--
		if s.logScroll < 0 {
			s.logScroll = 0
		}
	case "k", "up":
		if s.logScroll < maxScroll {
			s.logScroll++
		}
	case "esc":
		a.active = viewDashboard
	}
	return nil
}
