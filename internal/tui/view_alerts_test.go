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

func TestAlertViewFilterSeverity(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewAlerts
	s.Alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "a", Severity: "warning", FiredAt: 100},
		{ID: 2, RuleName: "b", Severity: "critical", FiredAt: 200},
		{ID: 3, RuleName: "c", Severity: "warning", FiredAt: 300},
	}

	// Initially no filter — all visible.
	filtered := s.Alertv.filteredAlerts()
	if len(filtered) != 3 {
		t.Errorf("no filter: got %d, want 3", len(filtered))
	}

	// Press f → warning.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if s.Alertv.filterSeverity != "warning" {
		t.Errorf("filterSeverity = %q, want warning", s.Alertv.filterSeverity)
	}
	filtered = s.Alertv.filteredAlerts()
	if len(filtered) != 2 {
		t.Errorf("warning filter: got %d, want 2", len(filtered))
	}

	// Press f → critical.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if s.Alertv.filterSeverity != "critical" {
		t.Errorf("filterSeverity = %q, want critical", s.Alertv.filterSeverity)
	}
	filtered = s.Alertv.filteredAlerts()
	if len(filtered) != 1 {
		t.Errorf("critical filter: got %d, want 1", len(filtered))
	}
	if filtered[0].RuleName != "b" {
		t.Errorf("filtered[0].RuleName = %q, want b", filtered[0].RuleName)
	}

	// Press f → back to all.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if s.Alertv.filterSeverity != "" {
		t.Errorf("filterSeverity = %q, want empty", s.Alertv.filterSeverity)
	}
}

func TestAlertViewFilterState(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewAlerts
	s.Alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "a", Severity: "warning", FiredAt: 100},                         // active
		{ID: 2, RuleName: "b", Severity: "critical", FiredAt: 200, Acknowledged: true},     // acknowledged
		{ID: 3, RuleName: "c", Severity: "warning", FiredAt: 300, ResolvedAt: 400},         // resolved
		{ID: 4, RuleName: "d", Severity: "critical", FiredAt: 500},                         // active
	}

	// F → active.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	if s.Alertv.filterState != "active" {
		t.Errorf("filterState = %q, want active", s.Alertv.filterState)
	}
	filtered := s.Alertv.filteredAlerts()
	if len(filtered) != 2 {
		t.Errorf("active filter: got %d, want 2", len(filtered))
	}

	// F → acknowledged.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	if s.Alertv.filterState != "acknowledged" {
		t.Errorf("filterState = %q, want acknowledged", s.Alertv.filterState)
	}
	filtered = s.Alertv.filteredAlerts()
	if len(filtered) != 1 {
		t.Errorf("acknowledged filter: got %d, want 1", len(filtered))
	}

	// F → resolved.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	if s.Alertv.filterState != "resolved" {
		t.Errorf("filterState = %q, want resolved", s.Alertv.filterState)
	}
	filtered = s.Alertv.filteredAlerts()
	if len(filtered) != 1 {
		t.Errorf("resolved filter: got %d, want 1", len(filtered))
	}

	// F → back to all.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	if s.Alertv.filterState != "" {
		t.Errorf("filterState = %q, want empty", s.Alertv.filterState)
	}
}

func TestAlertViewFilterCombined(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewAlerts
	s.Alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "a", Severity: "warning", FiredAt: 100},                     // active warning
		{ID: 2, RuleName: "b", Severity: "critical", FiredAt: 200},                    // active critical
		{ID: 3, RuleName: "c", Severity: "warning", FiredAt: 300, ResolvedAt: 400},    // resolved warning
		{ID: 4, RuleName: "d", Severity: "critical", FiredAt: 500, ResolvedAt: 600},   // resolved critical
	}

	// Set severity=critical, state=active.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")}) // warning
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")}) // critical
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}}) // active

	filtered := s.Alertv.filteredAlerts()
	if len(filtered) != 1 {
		t.Errorf("combined filter: got %d, want 1", len(filtered))
	}
	if filtered[0].RuleName != "b" {
		t.Errorf("filtered[0].RuleName = %q, want b", filtered[0].RuleName)
	}
}

func TestAlertViewFilterResetsCursor(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewAlerts
	s.Alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "a", Severity: "warning"},
		{ID: 2, RuleName: "b", Severity: "critical"},
	}
	s.Alertv.cursor = 1
	s.Alertv.scroll = 1
	s.Alertv.expandModal = &alertExpandModal{alert: s.Alertv.alerts[1]}

	// Pressing f should not reach the filter because the modal captures keys.
	// Close modal first, then press f.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyEscape})
	if s.Alertv.expandModal != nil {
		t.Error("esc should close expand modal")
	}

	// Re-set cursor state and press f.
	s.Alertv.cursor = 1
	s.Alertv.scroll = 1
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if s.Alertv.cursor != 0 {
		t.Errorf("cursor = %d, want 0", s.Alertv.cursor)
	}
	if s.Alertv.scroll != 0 {
		t.Errorf("scroll = %d, want 0", s.Alertv.scroll)
	}
}

func TestAlertViewRenderFilterTitle(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "a", Severity: "warning", FiredAt: 100},
		{ID: 2, RuleName: "b", Severity: "critical", FiredAt: 200},
	}
	s.Alertv.filterSeverity = "critical"

	got := renderAlertView(&a, s, 100, 20)
	plain := stripANSI(got)
	if !strings.Contains(plain, "1/2") {
		t.Error("title should show filtered/total count")
	}
	if !strings.Contains(plain, "[critical]") {
		t.Error("title should show active filter label")
	}
}

func TestAlertViewRenderNoMatchFilter(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "a", Severity: "warning", FiredAt: 100},
	}
	s.Alertv.filterSeverity = "critical"

	got := renderAlertView(&a, s, 100, 20)
	plain := stripANSI(got)
	if !strings.Contains(plain, "No alerts match") {
		t.Error("should show no-match message when filter excludes all")
	}
}

func TestAlertViewRenderSilencePicker(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Alertv.silenceMode = true
	s.Alertv.silenceRule = "test_rule"
	s.Alertv.alerts = []protocol.AlertMsg{{ID: 1, RuleName: "test_rule"}}

	got := renderSilencePicker(&s.Alertv, &a.theme)
	plain := stripANSI(got)
	if !strings.Contains(plain, "Silence") {
		t.Error("should show silence picker")
	}
	if !strings.Contains(plain, "5m") {
		t.Error("should show duration options")
	}
}
