package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/rook/internal/protocol"
)

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
