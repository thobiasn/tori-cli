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

func TestDetailScrollBounded(t *testing.T) {
	a := newTestApp()
	a.detail.containerID = "c1"
	a.detail.reset()

	// Push 3 entries.
	a.detail.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Message: "a"})
	a.detail.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Message: "b"})
	a.detail.logs.Push(protocol.LogEntryMsg{ContainerID: "c1", Message: "c"})

	// Scroll up repeatedly — should not exceed data length.
	for i := 0; i < 10; i++ {
		updateDetail(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
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

	updateDetail(&a, tea.KeyMsg{Type: tea.KeyEscape})
	if a.active != viewDashboard {
		t.Errorf("esc should switch to dashboard, got %d", a.active)
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
