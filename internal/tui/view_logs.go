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

// LogViewState holds state for the full-screen log viewer.
type LogViewState struct {
	logs   *RingBuffer[protocol.LogEntryMsg]
	scroll int // 0 = live tail (at bottom)
	live   bool

	// Filters.
	filterContainerID string
	filterProject     string
	filterStream      string // "", "stdout", "stderr"
	searchText        string
	searchMode        bool

	// Expanded line.
	expanded int // -1 = none

	// Backfill tracking.
	backfilled    bool
	oldestStreamTS int64 // oldest streaming entry timestamp
}

type logQueryMsg struct {
	entries []protocol.LogEntryMsg
}

func newLogViewState() LogViewState {
	return LogViewState{
		logs:     NewRingBuffer[protocol.LogEntryMsg](5000),
		live:     true,
		expanded: -1,
	}
}

func (s *LogViewState) onSwitch(c *Client) tea.Cmd {
	if s.backfilled {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		entries, err := c.QueryLogs(ctx, &protocol.QueryLogsReq{
			Start:       0,
			End:         time.Now().Unix(),
			ContainerID: s.filterContainerID,
			Limit:       500,
		})
		if err != nil {
			return logQueryMsg{}
		}
		return logQueryMsg{entries: entries}
	}
}

func (s *LogViewState) onStreamEntry(entry protocol.LogEntryMsg) {
	s.logs.Push(entry)
	if s.oldestStreamTS == 0 || entry.Timestamp < s.oldestStreamTS {
		s.oldestStreamTS = entry.Timestamp
	}
}

func (s *LogViewState) handleBackfill(msg logQueryMsg) {
	if s.backfilled || len(msg.entries) == 0 {
		s.backfilled = true
		return
	}

	// Only prepend entries older than the oldest streaming entry.
	var toInsert []protocol.LogEntryMsg
	if s.oldestStreamTS > 0 {
		for _, e := range msg.entries {
			if e.Timestamp < s.oldestStreamTS {
				toInsert = append(toInsert, e)
			}
		}
	} else {
		toInsert = msg.entries
	}

	if len(toInsert) > 0 {
		// Create a new buffer, insert backfill first, then existing streaming entries.
		existing := s.logs.Data()
		newBuf := NewRingBuffer[protocol.LogEntryMsg](5000)
		for _, e := range toInsert {
			newBuf.Push(e)
		}
		for _, e := range existing {
			newBuf.Push(e)
		}
		s.logs = newBuf
	}
	s.backfilled = true
}

func (s *LogViewState) matchesFilter(entry protocol.LogEntryMsg, contInfo []protocol.ContainerInfo) bool {
	if s.filterContainerID != "" && entry.ContainerID != s.filterContainerID {
		return false
	}
	if s.filterProject != "" {
		ids := projectContainerIDs(s.filterProject, contInfo)
		if !ids[entry.ContainerID] {
			return false
		}
	}
	if s.filterStream != "" && entry.Stream != s.filterStream {
		return false
	}
	if s.searchText != "" && !strings.Contains(strings.ToLower(entry.Message), strings.ToLower(s.searchText)) {
		return false
	}
	return true
}

func (s *LogViewState) filteredEntries(contInfo []protocol.ContainerInfo) []protocol.LogEntryMsg {
	all := s.logs.Data()
	if s.filterContainerID == "" && s.filterProject == "" && s.filterStream == "" && s.searchText == "" {
		return all
	}
	var out []protocol.LogEntryMsg
	for _, e := range all {
		if s.matchesFilter(e, contInfo) {
			out = append(out, e)
		}
	}
	return out
}

func projectContainerIDs(project string, contInfo []protocol.ContainerInfo) map[string]bool {
	ids := make(map[string]bool)
	for _, ci := range contInfo {
		if ci.Project == project {
			ids[ci.ID] = true
		}
	}
	return ids
}

// renderLogView renders the full-screen log view.
func renderLogView(a *App, width, height int) string {
	theme := &a.theme
	s := &a.logv

	filtered := s.filteredEntries(a.contInfo)
	innerH := height - 3 // box borders + footer
	if innerH < 1 {
		innerH = 1
	}
	innerW := width - 2

	// Determine visible slice based on scroll.
	var visible []protocol.LogEntryMsg
	if len(filtered) <= innerH {
		visible = filtered
	} else if s.scroll == 0 {
		// Live tail: show last innerH entries.
		visible = filtered[len(filtered)-innerH:]
	} else {
		end := len(filtered) - s.scroll
		if end < 0 {
			end = 0
		}
		start := end - innerH
		if start < 0 {
			start = 0
		}
		visible = filtered[start:end]
	}

	var lines []string
	for i, entry := range visible {
		if i == s.expanded {
			// Expanded: show full message.
			lines = append(lines, formatLogLine(entry, innerW, theme))
			wrapped := wrapText(entry.Message, innerW-2)
			for _, wl := range wrapped {
				lines = append(lines, "  "+wl)
			}
		} else {
			lines = append(lines, formatLogLine(entry, innerW, theme))
		}
	}

	// Title.
	title := "Logs"
	if s.filterContainerID != "" {
		name := containerNameByID(s.filterContainerID, a.contInfo)
		if name != "" {
			title += " -- " + name
		}
	} else if s.filterProject != "" {
		title += " -- " + s.filterProject
	} else {
		title += " -- all containers"
	}
	if s.scroll == 0 {
		title += " -- LIVE"
	} else {
		title += " -- PAUSED"
	}

	boxH := height - 1 // leave room for filter footer
	content := strings.Join(lines, "\n")
	box := Box(title, content, width, boxH, theme)

	// Filter footer.
	footer := renderLogFooter(s, innerW, theme)

	return box + "\n" + footer
}

func renderLogFooter(s *LogViewState, width int, theme *Theme) string {
	muted := lipgloss.NewStyle().Foreground(theme.Muted)

	contLabel := "all"
	if s.filterContainerID != "" {
		contLabel = Truncate(s.filterContainerID[:min(12, len(s.filterContainerID))], 12)
	}
	if s.filterProject != "" {
		contLabel = s.filterProject
	}

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

	footer := fmt.Sprintf(" c: %s | s: %s | %s | Esc clear | ? Help",
		muted.Render(contLabel), muted.Render(streamLabel), searchPart)
	return Truncate(footer, width)
}

func containerNameByID(id string, contInfo []protocol.ContainerInfo) string {
	for _, ci := range contInfo {
		if ci.ID == id {
			return ci.Name
		}
	}
	return ""
}

func wrapText(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	var lines []string
	for len(s) > width {
		lines = append(lines, s[:width])
		s = s[width:]
	}
	if len(s) > 0 {
		lines = append(lines, s)
	}
	return lines
}

// updateLogView handles keys in the log view.
func updateLogView(a *App, msg tea.KeyMsg) tea.Cmd {
	s := &a.logv
	key := msg.String()

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
			if len(key) == 1 {
				s.searchText += key
			}
		}
		return nil
	}

	switch key {
	case "j", "down":
		s.scroll--
		if s.scroll < 0 {
			s.scroll = 0
		}
	case "k", "up":
		s.scroll++
	case "g":
		// Cycle project filter (simplified: toggle off).
		if s.filterProject != "" {
			s.filterProject = ""
		}
	case "c":
		// Cycle container filter: all → each container → all.
		s.cycleContainerFilter(a.contInfo)
	case "s":
		// Toggle stream filter.
		switch s.filterStream {
		case "":
			s.filterStream = "stdout"
		case "stdout":
			s.filterStream = "stderr"
		default:
			s.filterStream = ""
		}
	case "/":
		s.searchMode = true
	case "enter":
		// Toggle expanded line.
		if s.expanded >= 0 {
			s.expanded = -1
		} else {
			// Expand bottom visible line.
			filtered := s.filteredEntries(a.contInfo)
			innerH := a.height - 4
			if innerH < 1 {
				innerH = 1
			}
			if len(filtered) > 0 {
				idx := len(filtered) - 1 - s.scroll
				if idx >= 0 && idx < len(filtered) {
					s.expanded = min(idx, innerH-1)
				}
			}
		}
	case "esc":
		// Clear filters.
		if s.searchText != "" {
			s.searchText = ""
		} else if s.filterContainerID != "" || s.filterProject != "" || s.filterStream != "" {
			s.filterContainerID = ""
			s.filterProject = ""
			s.filterStream = ""
		}
		s.scroll = 0
		s.expanded = -1
	}
	return nil
}

func (s *LogViewState) cycleContainerFilter(contInfo []protocol.ContainerInfo) {
	if len(contInfo) == 0 {
		return
	}
	if s.filterContainerID == "" {
		s.filterContainerID = contInfo[0].ID
		return
	}
	for i, ci := range contInfo {
		if ci.ID == s.filterContainerID {
			if i+1 < len(contInfo) {
				s.filterContainerID = contInfo[i+1].ID
			} else {
				s.filterContainerID = ""
			}
			return
		}
	}
	s.filterContainerID = ""
}
