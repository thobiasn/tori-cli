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
func renderLogView(a *App, s *Session, width, height int) string {
	theme := &a.theme
	lv := &s.Logv

	filtered := lv.filteredEntries(s.ContInfo)
	innerH := height - 3 // box borders + footer
	if innerH < 1 {
		innerH = 1
	}
	innerW := width - 2

	// Determine visible slice based on scroll.
	var visible []protocol.LogEntryMsg
	if len(filtered) <= innerH {
		visible = filtered
	} else if lv.scroll == 0 {
		// Live tail: show last innerH entries.
		visible = filtered[len(filtered)-innerH:]
	} else {
		end := len(filtered) - lv.scroll
		if end < 0 {
			end = 0
		}
		start := end - innerH
		if start < 0 {
			start = 0
		}
		visible = filtered[start:end]
	}

	// Calculate expansion lines so we can reduce visible entries if needed.
	cursorIdx := lv.cursor
	expandIdx := lv.expanded
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

	// Title.
	title := "Logs"
	if lv.filterContainerID != "" {
		name := containerNameByID(lv.filterContainerID, s.ContInfo)
		if name != "" {
			title += " ── " + name
		}
	} else if lv.filterProject != "" {
		title += " ── " + lv.filterProject
	} else {
		title += " ── all containers"
	}
	title += " ── " + FormatNumber(len(filtered)) + " lines"
	if lv.scroll == 0 {
		title += " ── LIVE"
	} else {
		title += " ── PAUSED"
	}

	boxH := height - 1 // leave room for filter footer
	content := strings.Join(lines, "\n")
	box := Box(title, content, width, boxH, theme)

	// Filter footer.
	footer := renderLogFooter(lv, innerW, theme)

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
func updateLogView(a *App, s *Session, msg tea.KeyMsg) tea.Cmd {
	lv := &s.Logv
	key := msg.String()

	// Search mode captures all keys.
	if lv.searchMode {
		switch key {
		case "enter", "esc":
			lv.searchMode = false
		case "backspace":
			if len(lv.searchText) > 0 {
				lv.searchText = lv.searchText[:len(lv.searchText)-1]
			}
		default:
			if len(key) == 1 && len(lv.searchText) < 128 {
				lv.searchText += key
			}
		}
		return nil
	}

	filtered := lv.filteredEntries(s.ContInfo)
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
		if lv.cursor == -1 {
			// Activate cursor at bottom, pause live tail.
			lv.cursor = visibleCount - 1
			if lv.cursor < 0 {
				lv.cursor = 0
			}
			if lv.scroll == 0 {
				lv.scroll = 0 // already at bottom, stay paused from cursor activation
			}
		} else if lv.cursor < visibleCount-1 {
			lv.cursor++
		} else if lv.scroll > 0 {
			lv.scroll--
		}
		lv.expanded = -1
	case "k", "up":
		if lv.cursor == -1 {
			lv.cursor = visibleCount - 1
			if lv.cursor < 0 {
				lv.cursor = 0
			}
		} else if lv.cursor > 0 {
			lv.cursor--
		} else if lv.scroll < maxScroll {
			lv.scroll++
		}
		lv.expanded = -1
	case "g":
		lv.cycleProjectFilter(s.ContInfo)
	case "c":
		lv.cycleContainerFilter(s.ContInfo)
	case "s":
		switch lv.filterStream {
		case "":
			lv.filterStream = "stdout"
		case "stdout":
			lv.filterStream = "stderr"
		default:
			lv.filterStream = ""
		}
	case "/":
		lv.searchMode = true
	case "enter":
		if lv.cursor >= 0 {
			if lv.expanded == lv.cursor {
				lv.expanded = -1
			} else {
				lv.expanded = lv.cursor
			}
		}
	case "esc":
		if lv.cursor >= 0 {
			lv.cursor = -1
			lv.expanded = -1
			lv.scroll = 0
		} else if lv.searchText != "" {
			lv.searchText = ""
		} else if lv.filterContainerID != "" || lv.filterProject != "" || lv.filterStream != "" {
			lv.filterContainerID = ""
			lv.filterProject = ""
			lv.filterStream = ""
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
