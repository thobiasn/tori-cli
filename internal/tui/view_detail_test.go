package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/thobiasn/rook/internal/protocol"
)

func TestDetailBackfillDedup(t *testing.T) {
	s := &DetailState{containerID: "c1"}
	s.reset()

	// Streaming entries arrive first.
	s.onStreamEntry(protocol.LogEntryMsg{Timestamp: 100, ContainerID: "c1", Message: "stream1"})
	s.onStreamEntry(protocol.LogEntryMsg{Timestamp: 101, ContainerID: "c1", Message: "stream2"})

	// Backfill with overlap.
	s.handleBackfill(detailLogQueryMsg{entries: []protocol.LogEntryMsg{
		{Timestamp: 90, ContainerID: "c1", Message: "old1"},
		{Timestamp: 100, ContainerID: "c1", Message: "dup"},
	}})

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

func TestDetailRestartConfirmationFlow(t *testing.T) {
	a := newTestApp()
	a.detail.containerID = "c1"
	a.detail.reset()

	// Press 'r' to initiate restart.
	updateDetail(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if !a.detail.confirmRestart {
		t.Fatal("'r' should trigger confirm restart")
	}

	// Press 'n' to cancel.
	updateDetail(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if a.detail.confirmRestart {
		t.Error("'n' should cancel confirm restart")
	}

	// Press 'r' again, then 'y' to confirm.
	updateDetail(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if !a.detail.confirmRestart {
		t.Fatal("'r' should trigger confirm restart")
	}
	cmd := updateDetail(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if a.detail.confirmRestart {
		t.Error("'y' should clear confirm restart")
	}
	if cmd == nil {
		t.Error("'y' should return a restart command")
	}
}

func TestDetailCursorBounded(t *testing.T) {
	a := newTestApp()
	a.detail.containerID = "c1"
	a.detail.reset()

	// Push 3 entries.
	a.detail.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Message: "a"})
	a.detail.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Message: "b"})
	a.detail.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Message: "c"})

	// Move cursor up repeatedly — should not go negative or exceed bounds.
	for i := 0; i < 10; i++ {
		updateDetail(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	}
	if a.detail.logCursor < 0 {
		t.Errorf("logCursor = %d, should not be negative", a.detail.logCursor)
	}
	if a.detail.logScroll > a.detail.logs.Len() {
		t.Errorf("logScroll = %d, exceeds data length %d", a.detail.logScroll, a.detail.logs.Len())
	}
}

func TestDetailEscBackToDashboard(t *testing.T) {
	a := newTestApp()
	a.active = viewDetail
	a.detail.containerID = "c1"
	a.detail.reset()

	// With no filters or cursor active, Esc goes directly to dashboard.
	updateDetail(&a, tea.KeyMsg{Type: tea.KeyEscape})
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
	if s.confirmRestart {
		t.Error("confirmRestart should be false")
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
	if s.searchText != "" {
		t.Errorf("searchText = %q, want empty", s.searchText)
	}
	if s.searchMode {
		t.Error("searchMode should be false")
	}
}

func TestDetailStreamFilter(t *testing.T) {
	a := newTestApp()
	a.detail.containerID = "c1"
	a.detail.reset()

	// Cycle: "" → stdout → stderr → ""
	updateDetail(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if a.detail.filterStream != "stdout" {
		t.Errorf("first cycle = %q, want stdout", a.detail.filterStream)
	}

	updateDetail(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if a.detail.filterStream != "stderr" {
		t.Errorf("second cycle = %q, want stderr", a.detail.filterStream)
	}

	updateDetail(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if a.detail.filterStream != "" {
		t.Errorf("third cycle = %q, want empty", a.detail.filterStream)
	}
}

func TestDetailSearchMode(t *testing.T) {
	a := newTestApp()
	a.detail.containerID = "c1"
	a.detail.reset()

	// Enter search mode.
	updateDetail(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !a.detail.searchMode {
		t.Fatal("/ should enter search mode")
	}

	// Type characters.
	updateDetail(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	updateDetail(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if a.detail.searchText != "er" {
		t.Errorf("searchText = %q, want er", a.detail.searchText)
	}

	// Backspace.
	updateDetail(&a, tea.KeyMsg{Type: tea.KeyBackspace})
	if a.detail.searchText != "e" {
		t.Errorf("searchText after backspace = %q, want e", a.detail.searchText)
	}

	// Enter exits search mode.
	updateDetail(&a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.detail.searchMode {
		t.Error("enter should exit search mode")
	}
	if a.detail.searchText != "e" {
		t.Errorf("searchText should persist after enter, got %q", a.detail.searchText)
	}
}

func TestDetailEscPriorityChain(t *testing.T) {
	a := newTestApp()
	a.active = viewDetail
	a.detail.containerID = "c1"
	a.detail.reset()

	// Set up all filter state.
	a.detail.searchText = "hello"
	a.detail.filterStream = "stdout"

	// First Esc: clear search text.
	updateDetail(&a, tea.KeyMsg{Type: tea.KeyEscape})
	if a.detail.searchText != "" {
		t.Errorf("first esc should clear searchText, got %q", a.detail.searchText)
	}
	if a.active != viewDetail {
		t.Error("should still be on detail view")
	}

	// Second Esc: clear stream filter.
	updateDetail(&a, tea.KeyMsg{Type: tea.KeyEscape})
	if a.detail.filterStream != "" {
		t.Errorf("second esc should clear filterStream, got %q", a.detail.filterStream)
	}
	if a.active != viewDetail {
		t.Error("should still be on detail view")
	}

	// Third Esc: back to dashboard.
	updateDetail(&a, tea.KeyMsg{Type: tea.KeyEscape})
	if a.active != viewDashboard {
		t.Errorf("third esc should go to dashboard, got %d", a.active)
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
