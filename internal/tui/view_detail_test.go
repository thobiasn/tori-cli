package tui

import (
	"testing"

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
}
