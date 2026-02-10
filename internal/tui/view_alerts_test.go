package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/thobiasn/rook/internal/protocol"
)

func TestAlertViewInitialStale(t *testing.T) {
	s := newAlertViewState()
	if !s.stale {
		t.Error("new alert view state should be stale")
	}
}

func TestAlertViewQueryResultClearsStale(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Alertv.stale = true

	alerts := []protocol.AlertMsg{
		{ID: 1, RuleName: "test", Severity: "warning", FiredAt: 100, Message: "hello"},
	}
	model, _ := a.Update(alertQueryMsg{alerts: alerts})
	a = model.(App)

	s = a.session()
	if s.Alertv.stale {
		t.Error("stale should be false after query result")
	}
	if len(s.Alertv.alerts) != 1 {
		t.Errorf("alerts count = %d, want 1", len(s.Alertv.alerts))
	}
}

func TestAlertViewCursorNavigation(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewAlerts
	s.Alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "a"}, {ID: 2, RuleName: "b"}, {ID: 3, RuleName: "c"},
	}
	s.Alertv.cursor = 0

	// Move down.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if s.Alertv.cursor != 1 {
		t.Errorf("cursor after j = %d, want 1", s.Alertv.cursor)
	}

	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if s.Alertv.cursor != 2 {
		t.Errorf("cursor after j = %d, want 2", s.Alertv.cursor)
	}

	// At end, shouldn't go further.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if s.Alertv.cursor != 2 {
		t.Errorf("cursor should stay at 2, got %d", s.Alertv.cursor)
	}

	// Move up.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if s.Alertv.cursor != 1 {
		t.Errorf("cursor after k = %d, want 1", s.Alertv.cursor)
	}
}

func TestAlertViewEscClears(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Alertv.cursor = 5
	s.Alertv.scroll = 3

	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyEscape})
	if s.Alertv.cursor != 0 || s.Alertv.scroll != 0 {
		t.Errorf("esc should reset cursor and scroll, got cursor=%d scroll=%d",
			s.Alertv.cursor, s.Alertv.scroll)
	}
}

func TestAlertViewSilencePickerFlow(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "high_cpu", FiredAt: 100},
	}
	s.Alertv.cursor = 0

	// Open silence picker.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if !s.Alertv.silenceMode {
		t.Fatal("should enter silence mode")
	}
	if s.Alertv.silenceRule != "high_cpu" {
		t.Errorf("silenceRule = %q, want high_cpu", s.Alertv.silenceRule)
	}

	// Navigate in picker.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if s.Alertv.silenceCursor != 1 {
		t.Errorf("silence cursor = %d, want 1", s.Alertv.silenceCursor)
	}

	// Escape cancels.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyEscape})
	if s.Alertv.silenceMode {
		t.Error("esc should exit silence mode")
	}
}

func TestAlertViewRenderEmpty(t *testing.T) {
	a := newTestApp()
	s := a.session()
	got := renderAlertView(&a, s, 80, 20)
	plain := stripANSI(got)
	if !strings.Contains(plain, "No alerts") {
		t.Error("empty alert view should show no alerts message")
	}
}

func TestAlertViewRenderWithAlerts(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "high_cpu", Severity: "critical", FiredAt: 1700000000, Message: "CPU 95%"},
		{ID: 2, RuleName: "disk_full", Severity: "warning", FiredAt: 1700000060, Message: "Disk 90%", ResolvedAt: 1700000120},
	}

	got := renderAlertView(&a, s, 100, 20)
	plain := stripANSI(got)
	if !strings.Contains(plain, "high_cpu") {
		t.Error("should contain high_cpu")
	}
	if !strings.Contains(plain, "RESOLVED") {
		t.Error("should show resolved status")
	}
}

func TestAlertViewRenderSilencePicker(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Alertv.silenceMode = true
	s.Alertv.silenceRule = "test_rule"
	s.Alertv.alerts = []protocol.AlertMsg{{ID: 1, RuleName: "test_rule"}}

	got := renderAlertView(&a, s, 80, 20)
	plain := stripANSI(got)
	if !strings.Contains(plain, "Silence") {
		t.Error("should show silence picker")
	}
	if !strings.Contains(plain, "5m") {
		t.Error("should show duration options")
	}
}
