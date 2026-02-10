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

	// Cursor and expanded line.
	cursor   int // -1 = inactive
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
		cursor:   -1,
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

	cursorStyle := lipgloss.NewStyle().Reverse(true)
	var lines []string
	for i, entry := range visible {
		line := formatLogLine(entry, innerW, theme)
		if i == s.cursor {
			line = cursorStyle.Render(line)
		}
		lines = append(lines, line)
		if i == s.expanded {
			wrapped := wrapText(entry.Message, innerW-2)
			for _, wl := range wrapped {
				lines = append(lines, "  "+wl)
			}
		}
	}

	// Title.
	title := "Logs"
	if s.filterContainerID != "" {
		name := containerNameByID(s.filterContainerID, a.contInfo)
		if name != "" {
			title += " ── " + name
		}
	} else if s.filterProject != "" {
		title += " ── " + s.filterProject
	} else {
		title += " ── all containers"
	}
	title += " ── " + FormatNumber(len(filtered)) + " lines"
	if s.scroll == 0 {
		title += " ── LIVE"
	} else {
		title += " ── PAUSED"
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

	projectLabel := "all"
	if s.filterProject != "" {
		projectLabel = s.filterProject
	}

	footer := fmt.Sprintf(" c: %s | g: %s | s: %s | %s | Esc clear | ? Help",
		muted.Render(contLabel), muted.Render(projectLabel), muted.Render(streamLabel), searchPart)
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
	runes := []rune(s)
	var lines []string
	for len(runes) > width {
		lines = append(lines, string(runes[:width]))
		runes = runes[width:]
	}
	if len(runes) > 0 {
		lines = append(lines, string(runes))
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
			if len(key) == 1 && len(s.searchText) < 128 {
				s.searchText += key
			}
		}
		return nil
	}

	filtered := s.filteredEntries(a.contInfo)
	innerH := a.height - 4
	if innerH < 1 {
		innerH = 1
	}
	visibleCount := len(filtered)
	if visibleCount > innerH {
		visibleCount = innerH
	}

	maxScroll := len(filtered) - innerH
	if maxScroll < 0 {
		maxScroll = 0
	}

	switch key {
	case "j", "down":
		if s.cursor == -1 {
			// Activate cursor at bottom, pause live tail.
			s.cursor = visibleCount - 1
			if s.cursor < 0 {
				s.cursor = 0
			}
			if s.scroll == 0 {
				s.scroll = 0 // already at bottom, stay paused from cursor activation
			}
		} else if s.cursor < visibleCount-1 {
			s.cursor++
		} else if s.scroll > 0 {
			s.scroll--
		}
		s.expanded = -1
	case "k", "up":
		if s.cursor == -1 {
			s.cursor = visibleCount - 1
			if s.cursor < 0 {
				s.cursor = 0
			}
		} else if s.cursor > 0 {
			s.cursor--
		} else if s.scroll < maxScroll {
			s.scroll++
		}
		s.expanded = -1
	case "g":
		s.cycleProjectFilter(a.contInfo)
	case "c":
		s.cycleContainerFilter(a.contInfo)
	case "s":
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
		if s.cursor >= 0 {
			if s.expanded == s.cursor {
				s.expanded = -1
			} else {
				s.expanded = s.cursor
			}
		}
	case "esc":
		if s.cursor >= 0 {
			s.cursor = -1
			s.expanded = -1
			s.scroll = 0
		} else if s.searchText != "" {
			s.searchText = ""
		} else if s.filterContainerID != "" || s.filterProject != "" || s.filterStream != "" {
			s.filterContainerID = ""
			s.filterProject = ""
			s.filterStream = ""
		}
	}
	return nil
}

func (s *LogViewState) cycleProjectFilter(contInfo []protocol.ContainerInfo) {
	// Collect unique project names.
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
