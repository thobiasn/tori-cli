package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/thobiasn/rook/internal/protocol"
)

func TestDetailBackfillDedup(t *testing.T) {
	s := &DetailState{containerID: "c1"}
	s.reset()

	// Streaming entries arrive first.
	s.onStreamEntry(protocol.LogEntryMsg{Timestamp: 100, ContainerID: "c1", Message: "stream1"})
	s.onStreamEntry(protocol.LogEntryMsg{Timestamp: 101, ContainerID: "c1", Message: "stream2"})

	// Backfill with overlap — agent returns DESC order (newest first).
	s.handleBackfill(detailLogQueryMsg{
		containerID: "c1",
		entries: []protocol.LogEntryMsg{
			{Timestamp: 100, ContainerID: "c1", Message: "dup"},
			{Timestamp: 90, ContainerID: "c1", Message: "old1"},
		},
	})

	data := s.logs.Data()
	if len(data) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(data))
	}
	if data[0].Message != "old1" {
		t.Errorf("data[0] = %q, want old1", data[0].Message)
	}
}

func TestDetailStreamEntryFiltersByContainer(t *testing.T) {
	s := &DetailState{containerID: "c1"}
	s.reset()

	s.onStreamEntry(protocol.LogEntryMsg{ContainerID: "c1", Message: "yes"})
	s.onStreamEntry(protocol.LogEntryMsg{ContainerID: "c2", Message: "no"})

	if s.logs.Len() != 1 {
		t.Errorf("expected 1 entry (filtered), got %d", s.logs.Len())
	}
}

func TestDetailCursorBounded(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Detail.containerID = "c1"
	s.Detail.reset()
	s.Detail.logFocused = true

	// Push 3 entries.
	s.Detail.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Message: "a"})
	s.Detail.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Message: "b"})
	s.Detail.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Message: "c"})

	// Move cursor up repeatedly — should not go negative or exceed bounds.
	for i := 0; i < 10; i++ {
		updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	}
	if s.Detail.logCursor < 0 {
		t.Errorf("logCursor = %d, should not be negative", s.Detail.logCursor)
	}
	if s.Detail.logScroll > s.Detail.logs.Len() {
		t.Errorf("logScroll = %d, exceeds data length %d", s.Detail.logScroll, s.Detail.logs.Len())
	}
}

func TestDetailTabFocusToggle(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewDetail
	s.Detail.containerID = "c1"
	s.Detail.reset()

	// Push some log entries.
	s.Detail.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Message: "a"})
	s.Detail.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Message: "b"})
	s.Detail.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Message: "c"})

	// Initially unfocused.
	if s.Detail.logFocused {
		t.Fatal("should start unfocused")
	}
	if s.Detail.logCursor != -1 {
		t.Fatalf("initial logCursor = %d, want -1", s.Detail.logCursor)
	}

	// Tab focuses logs and selects the latest (last) entry.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyTab})
	if !s.Detail.logFocused {
		t.Error("tab should focus logs")
	}
	if s.Detail.logCursor != 2 {
		t.Errorf("focused logCursor = %d, want 2 (last entry)", s.Detail.logCursor)
	}

	// Navigate cursor, set some state.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})

	// Tab unfocuses: resets cursor, scroll, expanded.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyTab})
	if s.Detail.logFocused {
		t.Error("second tab should unfocus logs")
	}
	if s.Detail.logCursor != -1 {
		t.Errorf("unfocused logCursor = %d, want -1", s.Detail.logCursor)
	}
	if s.Detail.logExpanded != -1 {
		t.Errorf("unfocused logExpanded = %d, want -1", s.Detail.logExpanded)
	}
	if s.Detail.logScroll != 0 {
		t.Errorf("unfocused logScroll = %d, want 0", s.Detail.logScroll)
	}

	// Should not have left the detail view.
	if a.active != viewDetail {
		t.Errorf("tab should not change view, got %d", a.active)
	}

	// Tab with empty logs focuses with cursor at 0.
	s.Detail.reset()
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyTab})
	if s.Detail.logCursor != 0 {
		t.Errorf("empty logs: focused logCursor = %d, want 0", s.Detail.logCursor)
	}
}

func TestDetailEscBackToDashboard(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewDetail
	s.Detail.containerID = "c1"
	s.Detail.reset()

	// With no filters or cursor active, Esc goes directly to dashboard.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyEscape})
	if a.active != viewDashboard {
		t.Errorf("esc with no state should switch to dashboard, got %d", a.active)
	}
}

func TestDetailReset(t *testing.T) {
	s := &DetailState{containerID: "c1"}
	s.reset()

	if s.logs == nil {
		t.Fatal("logs should be initialized")
	}
	if s.backfilled {
		t.Error("should not be backfilled after reset")
	}
	if s.logCursor != -1 {
		t.Errorf("logCursor = %d, want -1", s.logCursor)
	}
	if s.logExpanded != -1 {
		t.Errorf("logExpanded = %d, want -1", s.logExpanded)
	}
	if s.filterStream != "" {
		t.Errorf("filterStream = %q, want empty", s.filterStream)
	}
	if s.filterContainerID != "" {
		t.Errorf("filterContainerID = %q, want empty", s.filterContainerID)
	}
	if s.searchText != "" {
		t.Errorf("searchText = %q, want empty", s.searchText)
	}
	if s.filterFrom != 0 {
		t.Errorf("filterFrom = %d, want 0", s.filterFrom)
	}
	if s.filterTo != 0 {
		t.Errorf("filterTo = %d, want 0", s.filterTo)
	}
	if s.filterModal != nil {
		t.Error("filterModal should be nil")
	}
}

func TestDetailStreamFilter(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Detail.containerID = "c1"
	s.Detail.reset()

	// Cycle: "" → stdout → stderr → "" (works regardless of focus)
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if s.Detail.filterStream != "stdout" {
		t.Errorf("first cycle = %q, want stdout", s.Detail.filterStream)
	}

	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if s.Detail.filterStream != "stderr" {
		t.Errorf("second cycle = %q, want stderr", s.Detail.filterStream)
	}

	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if s.Detail.filterStream != "" {
		t.Errorf("third cycle = %q, want empty", s.Detail.filterStream)
	}
}

func TestDetailFilterModal(t *testing.T) {
	a := newTestAppWithDisplay()
	s := a.session()
	s.Detail.containerID = "c1"
	s.Detail.reset()

	// f key does nothing when logs are not focused.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if s.Detail.filterModal != nil {
		t.Fatal("f should not open filter when logs not focused")
	}

	// Focus logs, then open filter modal.
	s.Detail.logFocused = true
	s.Detail.logCursor = 0
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if s.Detail.filterModal == nil {
		t.Fatal("f should open filter modal when logs focused")
	}

	// Type text in first field (text, focus=0).
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if s.Detail.filterModal.text != "er" {
		t.Errorf("modal text = %q, want er", s.Detail.filterModal.text)
	}

	// Backspace.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyBackspace})
	if s.Detail.filterModal.text != "e" {
		t.Errorf("modal text after backspace = %q, want e", s.Detail.filterModal.text)
	}

	// Tab to from-date field (focus=1).
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyTab})
	if s.Detail.filterModal.focus != 1 {
		t.Errorf("focus after tab = %d, want 1", s.Detail.filterModal.focus)
	}

	// Type in from-date field — masked input accepts digits.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if !s.Detail.filterModal.fromDate.touched {
		t.Error("fromDate should be touched after typing")
	}

	// Tab cycles through from-time (2), to-date (3), to-time (4), back to text (0).
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyTab})
	if s.Detail.filterModal.focus != 2 {
		t.Errorf("focus = %d, want 2 (from-time)", s.Detail.filterModal.focus)
	}
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyTab})
	if s.Detail.filterModal.focus != 3 {
		t.Errorf("focus = %d, want 3 (to-date)", s.Detail.filterModal.focus)
	}
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyTab})
	if s.Detail.filterModal.focus != 4 {
		t.Errorf("focus = %d, want 4 (to-time)", s.Detail.filterModal.focus)
	}
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyTab})
	if s.Detail.filterModal.focus != 0 {
		t.Errorf("focus after full cycle = %d, want 0 (wrap)", s.Detail.filterModal.focus)
	}

	// Enter applies filter and closes modal.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyEnter})
	if s.Detail.filterModal != nil {
		t.Error("enter should close filter modal")
	}
	if s.Detail.searchText != "e" {
		t.Errorf("searchText should be applied, got %q", s.Detail.searchText)
	}
}

func TestDetailFilterModalEscCancels(t *testing.T) {
	a := newTestAppWithDisplay()
	s := a.session()
	s.Detail.containerID = "c1"
	s.Detail.reset()
	s.Detail.logFocused = true
	s.Detail.logCursor = 0
	s.Detail.searchText = "old"

	// Open filter modal.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if s.Detail.filterModal == nil {
		t.Fatal("should open filter modal")
	}

	// Type something new.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

	// Esc cancels without applying.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyEscape})
	if s.Detail.filterModal != nil {
		t.Error("esc should close filter modal")
	}
	if s.Detail.searchText != "old" {
		t.Errorf("searchText should remain %q, got %q", "old", s.Detail.searchText)
	}
}

func TestDetailEscPriorityChain(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewDetail
	s.Detail.containerID = "c1"
	s.Detail.reset()
	s.Detail.logFocused = true

	// Set up all filter state.
	s.Detail.searchText = "hello"
	s.Detail.filterStream = "stdout"

	// First Esc: clear search text, cursor resets to 0 (still focused).
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyEscape})
	if s.Detail.searchText != "" {
		t.Errorf("first esc should clear searchText, got %q", s.Detail.searchText)
	}
	if a.active != viewDetail {
		t.Error("should still be on detail view")
	}

	// Second Esc: clear stream filter, cursor resets to 0 (still focused).
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyEscape})
	if s.Detail.filterStream != "" {
		t.Errorf("second esc should clear filterStream, got %q", s.Detail.filterStream)
	}
	if a.active != viewDetail {
		t.Error("should still be on detail view")
	}

	// Third Esc: unfocus logs (cursor becomes -1).
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyEscape})
	if s.Detail.logFocused {
		t.Error("third esc should unfocus logs")
	}
	if s.Detail.logCursor != -1 {
		t.Errorf("unfocused logCursor = %d, want -1", s.Detail.logCursor)
	}
	if a.active != viewDetail {
		t.Error("should still be on detail view")
	}

	// Fourth Esc: back to dashboard.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyEscape})
	if a.active != viewDashboard {
		t.Errorf("fourth esc should go to dashboard, got %d", a.active)
	}
}

func TestDetailMatchesFilter(t *testing.T) {
	s := &DetailState{containerID: "c1"}
	s.reset()

	// No filters — everything matches.
	entry := protocol.LogEntryMsg{Stream: "stdout", Message: "hello world"}
	if !s.matchesFilter(entry) {
		t.Error("no filters should match everything")
	}

	// Stream filter.
	s.filterStream = "stderr"
	if s.matchesFilter(entry) {
		t.Error("stderr filter should not match stdout entry")
	}
	s.filterStream = "stdout"
	if !s.matchesFilter(entry) {
		t.Error("stdout filter should match stdout entry")
	}

	// Search filter (case-insensitive).
	s.filterStream = ""
	s.searchText = "HELLO"
	if !s.matchesFilter(entry) {
		t.Error("case-insensitive search should match")
	}
	s.searchText = "xyz"
	if s.matchesFilter(entry) {
		t.Error("non-matching search should not match")
	}

	// Combined filters.
	s.filterStream = "stdout"
	s.searchText = "hello"
	if !s.matchesFilter(entry) {
		t.Error("combined matching filters should match")
	}
	s.filterStream = "stderr"
	if s.matchesFilter(entry) {
		t.Error("stream mismatch should fail even with matching search")
	}
}

func TestDetailMatchesFilterContainer(t *testing.T) {
	s := &DetailState{project: "myapp", projectIDs: []string{"c1", "c2"}}
	s.reset()

	s.filterContainerID = "c1"
	e1 := protocol.LogEntryMsg{ContainerID: "c1", Message: "yes"}
	e2 := protocol.LogEntryMsg{ContainerID: "c2", Message: "no"}

	if !s.matchesFilter(e1) {
		t.Error("should match c1")
	}
	if s.matchesFilter(e2) {
		t.Error("should not match c2")
	}
}

func TestDetailFilteredData(t *testing.T) {
	s := &DetailState{containerID: "c1"}
	s.reset()

	s.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Stream: "stdout", Message: "out1"})
	s.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Stream: "stderr", Message: "err1"})
	s.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Stream: "stdout", Message: "out2"})

	// No filter.
	if len(s.filteredData()) != 3 {
		t.Errorf("no filter: got %d, want 3", len(s.filteredData()))
	}

	// Stream filter.
	s.filterStream = "stderr"
	data := s.filteredData()
	if len(data) != 1 || data[0].Message != "err1" {
		t.Errorf("stderr filter: got %d entries", len(data))
	}

	// Search filter.
	s.filterStream = ""
	s.searchText = "out"
	data = s.filteredData()
	if len(data) != 2 {
		t.Errorf("search 'out': got %d, want 2", len(data))
	}
}

func TestDetailGroupModeStreamEntry(t *testing.T) {
	s := &DetailState{
		project:    "myapp",
		projectIDs: []string{"c1", "c2"},
	}
	s.reset()

	// Entry from c1 (in project) should be accepted.
	s.onStreamEntry(protocol.LogEntryMsg{ContainerID: "c1", Message: "yes"})
	// Entry from c3 (not in project) should be rejected.
	s.onStreamEntry(protocol.LogEntryMsg{ContainerID: "c3", Message: "no"})
	// Entry from c2 (in project) should be accepted.
	s.onStreamEntry(protocol.LogEntryMsg{ContainerID: "c2", Message: "also yes"})

	if s.logs.Len() != 2 {
		t.Errorf("expected 2 entries in group mode, got %d", s.logs.Len())
	}
}

func TestDetailCycleContainerFilter(t *testing.T) {
	s := &DetailState{
		project:    "myapp",
		projectIDs: []string{"c1", "c2", "c3"},
	}
	s.reset()

	s.cycleContainerFilter(nil) // contInfo not needed for ID cycling
	if s.filterContainerID != "c1" {
		t.Errorf("first cycle = %q, want c1", s.filterContainerID)
	}

	s.cycleContainerFilter(nil)
	if s.filterContainerID != "c2" {
		t.Errorf("second cycle = %q, want c2", s.filterContainerID)
	}

	s.cycleContainerFilter(nil)
	if s.filterContainerID != "c3" {
		t.Errorf("third cycle = %q, want c3", s.filterContainerID)
	}

	s.cycleContainerFilter(nil)
	if s.filterContainerID != "" {
		t.Errorf("fourth cycle = %q, want empty", s.filterContainerID)
	}
}

func TestDetailCycleContainerFilterSingleMode(t *testing.T) {
	s := &DetailState{containerID: "c1"}
	s.reset()

	// In single-container mode, cycling should be a no-op.
	s.cycleContainerFilter(nil)
	if s.filterContainerID != "" {
		t.Errorf("should stay empty in single mode, got %q", s.filterContainerID)
	}
}

func TestDetailIsGroupMode(t *testing.T) {
	single := &DetailState{containerID: "c1"}
	if single.isGroupMode() {
		t.Error("single container should not be group mode")
	}

	group := &DetailState{project: "myapp"}
	if !group.isGroupMode() {
		t.Error("project set should be group mode")
	}

	both := &DetailState{containerID: "c1", project: "myapp"}
	if both.isGroupMode() {
		t.Error("containerID set should not be group mode even with project")
	}
}

func TestInjectDeploySeparators(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		out := injectDeploySeparators(nil)
		if len(out) != 0 {
			t.Errorf("expected empty, got %d entries", len(out))
		}
	})

	t.Run("same container", func(t *testing.T) {
		entries := []protocol.LogEntryMsg{
			{Timestamp: 1, ContainerID: "aaa", ContainerName: "web", Stream: "stdout", Message: "line1"},
			{Timestamp: 2, ContainerID: "aaa", ContainerName: "web", Stream: "stdout", Message: "line2"},
			{Timestamp: 3, ContainerID: "aaa", ContainerName: "web", Stream: "stderr", Message: "line3"},
		}
		out := injectDeploySeparators(entries)
		if len(out) != 3 {
			t.Errorf("same container should not add separators, got %d entries", len(out))
		}
	})

	t.Run("two containers", func(t *testing.T) {
		entries := []protocol.LogEntryMsg{
			{Timestamp: 1, ContainerID: "old-c", ContainerName: "web-old", Stream: "stdout", Message: "from old"},
			{Timestamp: 2, ContainerID: "old-c", ContainerName: "web-old", Stream: "stdout", Message: "from old 2"},
			{Timestamp: 3, ContainerID: "new-c", ContainerName: "web-new", Stream: "stdout", Message: "from new"},
			{Timestamp: 4, ContainerID: "new-c", ContainerName: "web-new", Stream: "stdout", Message: "from new 2"},
		}
		out := injectDeploySeparators(entries)

		// Expect: old1, old2, separator, new1, new2 = 5 entries.
		if len(out) != 5 {
			t.Fatalf("expected 5 entries (4 + 1 separator), got %d", len(out))
		}

		sep := out[2]
		if sep.Stream != "event" {
			t.Errorf("separator Stream = %q, want event", sep.Stream)
		}
		if sep.ContainerID != "new-c" {
			t.Errorf("separator ContainerID = %q, want new-c", sep.ContainerID)
		}
		if sep.Timestamp != 3 {
			t.Errorf("separator Timestamp = %d, want 3", sep.Timestamp)
		}
		if !strings.Contains(sep.Message, "redeployed") {
			t.Errorf("separator Message = %q, should contain 'redeployed'", sep.Message)
		}
	})
}

func TestDetailResetClearsServiceFields(t *testing.T) {
	det := &DetailState{containerID: "c1"}
	det.metricsBackfilled = true

	det.reset()

	if det.metricsBackfilled {
		t.Error("metricsBackfilled should be false after reset")
	}
}

func TestMaskedField(t *testing.T) {
	now := time.Date(2024, 6, 15, 13, 45, 22, 0, time.Local)

	t.Run("initial state", func(t *testing.T) {
		f := newMaskedField("15:04:05", now)
		if f.touched {
			t.Error("should not be touched initially")
		}
		if f.resolved() != "" {
			t.Error("untouched field should resolve to empty")
		}
		// Display shows defaults.
		display := f.render(false, &Theme{})
		if !strings.Contains(stripANSI(display), "13:45:22") {
			t.Errorf("display should show defaults, got %q", stripANSI(display))
		}
	})

	t.Run("type digits", func(t *testing.T) {
		f := newMaskedField("15:04:05", now)
		f.typeRune('0')
		f.typeRune('8')
		// Typed "08" for hours, cursor should auto-skip the ':'.
		if !f.touched {
			t.Error("should be touched after typing")
		}
		resolved := f.resolved()
		// "08:45:22" — typed hours, defaults for min/sec.
		if resolved != "08:45:22" {
			t.Errorf("resolved = %q, want 08:45:22", resolved)
		}
	})

	t.Run("type and backspace", func(t *testing.T) {
		f := newMaskedField("15:04:05", now)
		f.typeRune('0')
		f.typeRune('8')
		f.typeRune('3')
		f.typeRune('0')
		// "08:30:22"
		if f.resolved() != "08:30:22" {
			t.Errorf("got %q", f.resolved())
		}
		// Backspace removes last typed digit (min[1], pos 4).
		// Default for pos 4 is '5' (from "13:45:22").
		f.backspace()
		if f.resolved() != "08:35:22" {
			t.Errorf("after backspace: %q, want 08:35:22", f.resolved())
		}
	})

	t.Run("backspace all restores untouched", func(t *testing.T) {
		f := newMaskedField("15:04:05", now)
		f.typeRune('0')
		f.backspace()
		if f.touched {
			t.Error("should be untouched after removing all typed digits")
		}
		if f.resolved() != "" {
			t.Error("should resolve to empty when untouched")
		}
	})

	t.Run("non-digit rejected", func(t *testing.T) {
		f := newMaskedField("15:04:05", now)
		f.typeRune('a')
		if f.touched {
			t.Error("non-digit should not touch the field")
		}
	})

	t.Run("fill from existing value", func(t *testing.T) {
		f := newMaskedField("2006-01-02", now)
		f.fill("2024-01-15")
		if !f.touched {
			t.Error("fill should mark as touched")
		}
		if f.resolved() != "2024-01-15" {
			t.Errorf("resolved = %q", f.resolved())
		}
	})

	t.Run("date format", func(t *testing.T) {
		f := newMaskedField("2006-01-02", now)
		// Type year "2025".
		for _, r := range "2025" {
			f.typeRune(r)
		}
		// Should fill month/day from defaults (06/15).
		if f.resolved() != "2025-06-15" {
			t.Errorf("resolved = %q, want 2025-06-15", f.resolved())
		}
	})
}

func TestParseFilterBound(t *testing.T) {
	df := "2006-01-02"
	tf := "15:04:05"

	// Full date + time.
	ts := parseFilterBound("2024-01-15", "08:00:00", df, tf, false)
	if ts == 0 {
		t.Error("full bound should be non-zero")
	}

	// Time only → today's date.
	ts = parseFilterBound("", "13:00:00", df, tf, false)
	if ts == 0 {
		t.Error("time-only from bound should be non-zero")
	}

	// Date only from → start of day.
	ts = parseFilterBound("2024-01-15", "", df, tf, false)
	expected := time.Date(2024, 1, 15, 0, 0, 0, 0, time.Local).Unix()
	if ts != expected {
		t.Errorf("date-only from = %d, want %d", ts, expected)
	}

	// Date only to → end of day.
	ts = parseFilterBound("2024-01-15", "", df, tf, true)
	expected = time.Date(2024, 1, 15, 23, 59, 59, 0, time.Local).Unix()
	if ts != expected {
		t.Errorf("date-only to = %d, want %d", ts, expected)
	}

	// Both empty → 0.
	if parseFilterBound("", "", df, tf, false) != 0 {
		t.Error("empty inputs should return 0")
	}
}

func TestDetailBackfillOrdering(t *testing.T) {
	s := &DetailState{containerID: "c1"}
	s.reset()

	// Simulate agent returning DESC order (newest first).
	s.handleBackfill(detailLogQueryMsg{
		containerID: "c1",
		entries: []protocol.LogEntryMsg{
			{Timestamp: 300, ContainerID: "c1", Message: "c"},
			{Timestamp: 200, ContainerID: "c1", Message: "b"},
			{Timestamp: 100, ContainerID: "c1", Message: "a"},
		},
	})

	// After backfill, entries should be in ASC (chronological) order.
	data := s.logs.Data()
	if len(data) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(data))
	}
	if data[0].Message != "a" || data[1].Message != "b" || data[2].Message != "c" {
		t.Errorf("entries not in chronological order: [%s, %s, %s]",
			data[0].Message, data[1].Message, data[2].Message)
	}

	// Streaming entries should appear after backfilled entries.
	s.onStreamEntry(protocol.LogEntryMsg{Timestamp: 400, ContainerID: "c1", Message: "d"})
	data = s.logs.Data()
	if len(data) != 4 {
		t.Fatalf("expected 4 entries after stream, got %d", len(data))
	}
	if data[3].Message != "d" {
		t.Errorf("streaming entry should be last, got %q", data[3].Message)
	}
}

