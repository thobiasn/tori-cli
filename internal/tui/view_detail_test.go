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
	s.handleBackfill(detailLogQueryMsg{entries: []protocol.LogEntryMsg{
		{Timestamp: 100, ContainerID: "c1", Message: "dup"},
		{Timestamp: 90, ContainerID: "c1", Message: "old1"},
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

func TestDetailCursorBounded(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Detail.containerID = "c1"
	s.Detail.reset()

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
	if s.searchMode {
		t.Error("searchMode should be false")
	}
}

func TestDetailStreamFilter(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Detail.containerID = "c1"
	s.Detail.reset()

	// Cycle: "" → stdout → stderr → ""
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

func TestDetailSearchMode(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Detail.containerID = "c1"
	s.Detail.reset()

	// Enter search mode.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !s.Detail.searchMode {
		t.Fatal("/ should enter search mode")
	}

	// Type characters.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if s.Detail.searchText != "er" {
		t.Errorf("searchText = %q, want er", s.Detail.searchText)
	}

	// Backspace.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyBackspace})
	if s.Detail.searchText != "e" {
		t.Errorf("searchText after backspace = %q, want e", s.Detail.searchText)
	}

	// Enter exits search mode.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyEnter})
	if s.Detail.searchMode {
		t.Error("enter should exit search mode")
	}
	if s.Detail.searchText != "e" {
		t.Errorf("searchText should persist after enter, got %q", s.Detail.searchText)
	}
}

func TestDetailEscPriorityChain(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewDetail
	s.Detail.containerID = "c1"
	s.Detail.reset()

	// Set up all filter state.
	s.Detail.searchText = "hello"
	s.Detail.filterStream = "stdout"

	// First Esc: clear search text.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyEscape})
	if s.Detail.searchText != "" {
		t.Errorf("first esc should clear searchText, got %q", s.Detail.searchText)
	}
	if a.active != viewDetail {
		t.Error("should still be on detail view")
	}

	// Second Esc: clear stream filter.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyEscape})
	if s.Detail.filterStream != "" {
		t.Errorf("second esc should clear filterStream, got %q", s.Detail.filterStream)
	}
	if a.active != viewDetail {
		t.Error("should still be on detail view")
	}

	// Third Esc: back to dashboard.
	updateDetail(&a, s, tea.KeyMsg{Type: tea.KeyEscape})
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
	det.deployTimestamps = []int64{100, 200}

	det.reset()

	if det.metricsBackfilled {
		t.Error("metricsBackfilled should be false after reset")
	}
	if det.deployTimestamps != nil {
		t.Errorf("deployTimestamps should be nil after reset, got %v", det.deployTimestamps)
	}
}

func TestDetailBackfillOrdering(t *testing.T) {
	s := &DetailState{containerID: "c1"}
	s.reset()

	// Simulate agent returning DESC order (newest first).
	s.handleBackfill(detailLogQueryMsg{entries: []protocol.LogEntryMsg{
		{Timestamp: 300, ContainerID: "c1", Message: "c"},
		{Timestamp: 200, ContainerID: "c1", Message: "b"},
		{Timestamp: 100, ContainerID: "c1", Message: "a"},
	}})

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

func TestDeployVLines(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		vl := deployVLines(nil, 100, 3600)
		if len(vl) != 0 {
			t.Errorf("expected no vlines for nil timestamps, got %d", len(vl))
		}
	})

	t.Run("no data", func(t *testing.T) {
		vl := deployVLines([]int64{100}, 0, 3600)
		if len(vl) != 0 {
			t.Errorf("expected no vlines for zero dataLen, got %d", len(vl))
		}
	})

	t.Run("within window", func(t *testing.T) {
		now := time.Now().Unix()
		// Place a deploy marker at the midpoint of a 1h window.
		ts := now - 1800
		vl := deployVLines([]int64{ts}, 100, 3600)
		if len(vl) != 1 {
			t.Fatalf("expected 1 vline, got %d", len(vl))
		}
		// Frac should be approximately 0.5 (midpoint).
		if vl[0].Frac < 0.4 || vl[0].Frac > 0.6 {
			t.Errorf("frac = %f, want ~0.5", vl[0].Frac)
		}
		if vl[0].Label != "↻" {
			t.Errorf("label = %q, want ↻", vl[0].Label)
		}
	})

	t.Run("outside window filtered", func(t *testing.T) {
		now := time.Now().Unix()
		// Timestamp well outside the window (2 hours ago in a 1h window).
		ts := now - 7200
		vl := deployVLines([]int64{ts}, 100, 3600)
		if len(vl) != 0 {
			t.Errorf("expected timestamp outside window to be filtered, got %d", len(vl))
		}
	})

	t.Run("live mode infers range", func(t *testing.T) {
		now := time.Now().Unix()
		// windowSec=0 → range inferred from dataLen*10.
		// dataLen=100 → 1000s range. Place marker at 500s ago.
		ts := now - 500
		vl := deployVLines([]int64{ts}, 100, 0)
		if len(vl) != 1 {
			t.Fatalf("expected 1 vline in live mode, got %d", len(vl))
		}
		if vl[0].Frac < 0.4 || vl[0].Frac > 0.6 {
			t.Errorf("frac = %f, want ~0.5", vl[0].Frac)
		}
	})
}
