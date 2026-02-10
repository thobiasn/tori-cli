package tui

import (
	"testing"

	"github.com/thobiasn/rook/internal/protocol"
)

func TestLogViewBackfillDedup(t *testing.T) {
	s := newLogViewState()

	// Simulate streaming entries arriving first.
	s.onStreamEntry(protocol.LogEntryMsg{Timestamp: 100, Message: "stream1"})
	s.onStreamEntry(protocol.LogEntryMsg{Timestamp: 101, Message: "stream2"})

	if s.oldestStreamTS != 100 {
		t.Fatalf("oldestStreamTS = %d, want 100", s.oldestStreamTS)
	}

	// Backfill arrives with some overlap.
	s.handleBackfill(logQueryMsg{entries: []protocol.LogEntryMsg{
		{Timestamp: 90, Message: "old1"},
		{Timestamp: 95, Message: "old2"},
		{Timestamp: 100, Message: "dup"},  // should be skipped
		{Timestamp: 101, Message: "dup2"}, // should be skipped
	}})

	if !s.backfilled {
		t.Fatal("should be marked as backfilled")
	}

	data := s.logs.Data()
	// Should have: old1, old2, stream1, stream2 (4 entries, not 6).
	if len(data) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(data))
	}
	if data[0].Message != "old1" {
		t.Errorf("data[0] = %q, want old1", data[0].Message)
	}
	if data[1].Message != "old2" {
		t.Errorf("data[1] = %q, want old2", data[1].Message)
	}
	if data[2].Message != "stream1" {
		t.Errorf("data[2] = %q, want stream1", data[2].Message)
	}
	if data[3].Message != "stream2" {
		t.Errorf("data[3] = %q, want stream2", data[3].Message)
	}
}

func TestLogViewBackfillNoStreaming(t *testing.T) {
	s := newLogViewState()

	// No streaming entries yet.
	s.handleBackfill(logQueryMsg{entries: []protocol.LogEntryMsg{
		{Timestamp: 90, Message: "old1"},
		{Timestamp: 95, Message: "old2"},
	}})

	data := s.logs.Data()
	if len(data) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(data))
	}
}

func TestLogViewBackfillIdempotent(t *testing.T) {
	s := newLogViewState()
	s.handleBackfill(logQueryMsg{entries: []protocol.LogEntryMsg{
		{Timestamp: 90, Message: "old1"},
	}})

	// Second backfill should be ignored.
	s.handleBackfill(logQueryMsg{entries: []protocol.LogEntryMsg{
		{Timestamp: 80, Message: "older"},
	}})

	data := s.logs.Data()
	if len(data) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(data))
	}
}

func TestLogViewMatchesFilterContainer(t *testing.T) {
	s := newLogViewState()
	s.filterContainerID = "c1"

	entry1 := protocol.LogEntryMsg{ContainerID: "c1", Message: "yes"}
	entry2 := protocol.LogEntryMsg{ContainerID: "c2", Message: "no"}

	if !s.matchesFilter(entry1, nil) {
		t.Error("should match c1")
	}
	if s.matchesFilter(entry2, nil) {
		t.Error("should not match c2")
	}
}

func TestLogViewMatchesFilterProject(t *testing.T) {
	s := newLogViewState()
	s.filterProject = "myapp"

	contInfo := []protocol.ContainerInfo{
		{ID: "c1", Project: "myapp"},
		{ID: "c2", Project: "other"},
	}

	entry1 := protocol.LogEntryMsg{ContainerID: "c1", Message: "yes"}
	entry2 := protocol.LogEntryMsg{ContainerID: "c2", Message: "no"}

	if !s.matchesFilter(entry1, contInfo) {
		t.Error("should match project myapp container")
	}
	if s.matchesFilter(entry2, contInfo) {
		t.Error("should not match different project")
	}
}

func TestLogViewMatchesFilterStream(t *testing.T) {
	s := newLogViewState()
	s.filterStream = "stderr"

	entry1 := protocol.LogEntryMsg{Stream: "stderr", Message: "err"}
	entry2 := protocol.LogEntryMsg{Stream: "stdout", Message: "out"}

	if !s.matchesFilter(entry1, nil) {
		t.Error("should match stderr")
	}
	if s.matchesFilter(entry2, nil) {
		t.Error("should not match stdout")
	}
}

func TestLogViewMatchesFilterSearch(t *testing.T) {
	s := newLogViewState()
	s.searchText = "error"

	entry1 := protocol.LogEntryMsg{Message: "an Error occurred"}
	entry2 := protocol.LogEntryMsg{Message: "all good"}

	if !s.matchesFilter(entry1, nil) {
		t.Error("should match case-insensitive search")
	}
	if s.matchesFilter(entry2, nil) {
		t.Error("should not match")
	}
}

func TestLogViewCycleContainerFilter(t *testing.T) {
	s := newLogViewState()
	contInfo := []protocol.ContainerInfo{
		{ID: "c1"}, {ID: "c2"}, {ID: "c3"},
	}

	s.cycleContainerFilter(contInfo)
	if s.filterContainerID != "c1" {
		t.Errorf("first cycle = %q, want c1", s.filterContainerID)
	}

	s.cycleContainerFilter(contInfo)
	if s.filterContainerID != "c2" {
		t.Errorf("second cycle = %q, want c2", s.filterContainerID)
	}

	s.cycleContainerFilter(contInfo)
	if s.filterContainerID != "c3" {
		t.Errorf("third cycle = %q, want c3", s.filterContainerID)
	}

	s.cycleContainerFilter(contInfo)
	if s.filterContainerID != "" {
		t.Errorf("fourth cycle = %q, want empty (all)", s.filterContainerID)
	}
}
