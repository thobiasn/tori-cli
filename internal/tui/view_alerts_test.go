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
	a.alertv.stale = true

	alerts := []protocol.AlertMsg{
		{ID: 1, RuleName: "test", Severity: "warning", FiredAt: 100, Message: "hello"},
	}
	model, _ := a.Update(alertQueryMsg{alerts: alerts})
	a = model.(App)

	if a.alertv.stale {
		t.Error("stale should be false after query result")
	}
	if len(a.alertv.alerts) != 1 {
		t.Errorf("alerts count = %d, want 1", len(a.alertv.alerts))
	}
}

func TestAlertViewCursorNavigation(t *testing.T) {
	a := newTestApp()
	a.active = viewAlerts
	a.alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "a"}, {ID: 2, RuleName: "b"}, {ID: 3, RuleName: "c"},
	}
	a.alertv.cursor = 0

	// Move down.
	updateAlertView(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if a.alertv.cursor != 1 {
		t.Errorf("cursor after j = %d, want 1", a.alertv.cursor)
	}

	updateAlertView(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if a.alertv.cursor != 2 {
		t.Errorf("cursor after j = %d, want 2", a.alertv.cursor)
	}

	// At end, shouldn't go further.
	updateAlertView(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if a.alertv.cursor != 2 {
		t.Errorf("cursor should stay at 2, got %d", a.alertv.cursor)
	}

	// Move up.
	updateAlertView(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if a.alertv.cursor != 1 {
		t.Errorf("cursor after k = %d, want 1", a.alertv.cursor)
	}
}

func TestAlertViewEscClears(t *testing.T) {
	a := newTestApp()
	a.alertv.cursor = 5
	a.alertv.scroll = 3

	updateAlertView(&a, tea.KeyMsg{Type: tea.KeyEscape})
	if a.alertv.cursor != 0 || a.alertv.scroll != 0 {
		t.Errorf("esc should reset cursor and scroll, got cursor=%d scroll=%d",
			a.alertv.cursor, a.alertv.scroll)
	}
}

func TestAlertViewSilencePickerFlow(t *testing.T) {
	a := newTestApp()
	a.alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "high_cpu", FiredAt: 100},
	}
	a.alertv.cursor = 0

	// Open silence picker.
	updateAlertView(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if !a.alertv.silenceMode {
		t.Fatal("should enter silence mode")
	}
	if a.alertv.silenceRule != "high_cpu" {
		t.Errorf("silenceRule = %q, want high_cpu", a.alertv.silenceRule)
	}

	// Navigate in picker.
	updateAlertView(&a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if a.alertv.silenceCursor != 1 {
		t.Errorf("silence cursor = %d, want 1", a.alertv.silenceCursor)
	}

	// Escape cancels.
	updateAlertView(&a, tea.KeyMsg{Type: tea.KeyEscape})
	if a.alertv.silenceMode {
		t.Error("esc should exit silence mode")
	}
}

func TestAlertViewRenderEmpty(t *testing.T) {
	a := newTestApp()
	got := renderAlertView(&a, 80, 20)
	plain := stripANSI(got)
	if !strings.Contains(plain, "No alerts") {
		t.Error("empty alert view should show no alerts message")
	}
}

func TestAlertViewRenderWithAlerts(t *testing.T) {
	a := newTestApp()
	a.alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "high_cpu", Severity: "critical", FiredAt: 1700000000, Message: "CPU 95%"},
		{ID: 2, RuleName: "disk_full", Severity: "warning", FiredAt: 1700000060, Message: "Disk 90%", ResolvedAt: 1700000120},
	}

	got := renderAlertView(&a, 100, 20)
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
	a.alertv.silenceMode = true
	a.alertv.silenceRule = "test_rule"
	a.alertv.alerts = []protocol.AlertMsg{{ID: 1, RuleName: "test_rule"}}

	got := renderAlertView(&a, 80, 20)
	plain := stripANSI(got)
	if !strings.Contains(plain, "Silence") {
		t.Error("should show silence picker")
	}
	if !strings.Contains(plain, "5m") {
		t.Error("should show duration options")
	}
}
