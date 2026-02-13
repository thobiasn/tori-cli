package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

func TestAlertViewInitialStale(t *testing.T) {
	s := newAlertViewState()
	if !s.stale {
		t.Error("new alert view state should be stale")
	}
	if !s.rulesStale {
		t.Error("new alert view state should have rulesStale")
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
		{ID: 1, RuleName: "a", Severity: "critical", FiredAt: 100},
		{ID: 2, RuleName: "b", Severity: "critical", FiredAt: 200},
		{ID: 3, RuleName: "c", Severity: "critical", FiredAt: 300},
	}

	// Items: header(FIRING), a, b, c => cursor should start at 1 (first data row).
	items := buildSectionItems(s.Alertv.alerts, false)
	clampCursorToItems(&s.Alertv, items)
	if s.Alertv.cursor != 1 {
		t.Fatalf("initial cursor = %d, want 1 (first data row)", s.Alertv.cursor)
	}

	// Move down.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if s.Alertv.cursor != 2 {
		t.Errorf("cursor after j = %d, want 2", s.Alertv.cursor)
	}

	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if s.Alertv.cursor != 3 {
		t.Errorf("cursor after j = %d, want 3", s.Alertv.cursor)
	}

	// At end, shouldn't go further.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if s.Alertv.cursor != 3 {
		t.Errorf("cursor should stay at 3, got %d", s.Alertv.cursor)
	}

	// Move up.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if s.Alertv.cursor != 2 {
		t.Errorf("cursor after k = %d, want 2", s.Alertv.cursor)
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
		{ID: 1, RuleName: "high_cpu", Severity: "critical", FiredAt: 100},
	}
	// Clamp cursor to first data row.
	items := buildSectionItems(s.Alertv.alerts, false)
	clampCursorToItems(&s.Alertv, items)

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
	// Resolved section is collapsed by default; count should be shown.
	if !strings.Contains(plain, "RESOLVED (1)") {
		t.Error("should show resolved count")
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

func TestAlertViewSubViewToggle(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewAlerts

	if s.Alertv.subView != 0 {
		t.Fatal("should start on alerts sub-view")
	}

	// Tab switches to rules.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyTab})
	if s.Alertv.subView != 1 {
		t.Error("tab should switch to rules sub-view")
	}

	// Tab switches back.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyTab})
	if s.Alertv.subView != 0 {
		t.Error("tab should switch back to alerts sub-view")
	}
}

func TestAlertViewResolvedToggle(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewAlerts
	s.Alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "a", Severity: "critical", FiredAt: 100},
		{ID: 2, RuleName: "b", Severity: "warning", FiredAt: 200, ResolvedAt: 300},
	}

	// Initially resolved is collapsed.
	items := buildSectionItems(s.Alertv.alerts, s.Alertv.showResolved)
	// Should have: FIRING header, alert a, RESOLVED header (collapsed).
	resolvedRows := 0
	for _, item := range items {
		if !item.isHeader && item.alert.ResolvedAt > 0 {
			resolvedRows++
		}
	}
	if resolvedRows != 0 {
		t.Errorf("resolved rows = %d, want 0 (collapsed)", resolvedRows)
	}

	// Press r to expand.
	clampCursorToItems(&s.Alertv, items)
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if !s.Alertv.showResolved {
		t.Error("r should toggle showResolved to true")
	}
	items = buildSectionItems(s.Alertv.alerts, s.Alertv.showResolved)
	resolvedRows = 0
	for _, item := range items {
		if !item.isHeader && item.alert.ResolvedAt > 0 {
			resolvedRows++
		}
	}
	if resolvedRows != 1 {
		t.Errorf("resolved rows = %d, want 1 (expanded)", resolvedRows)
	}

	// Press r again to collapse.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if s.Alertv.showResolved {
		t.Error("r should toggle showResolved back to false")
	}
}

func TestAlertViewSectionGrouping(t *testing.T) {
	alerts := []protocol.AlertMsg{
		{ID: 1, RuleName: "a", Severity: "critical", FiredAt: 100},                         // firing
		{ID: 2, RuleName: "b", Severity: "warning", FiredAt: 200, Acknowledged: true},       // acked
		{ID: 3, RuleName: "c", Severity: "critical", FiredAt: 300, ResolvedAt: 400},          // resolved
		{ID: 4, RuleName: "d", Severity: "warning", FiredAt: 500},                            // firing
	}

	items := buildSectionItems(alerts, true) // showResolved=true

	// Expected: FIRING header, a, d, ACK header, b, RESOLVED header, c
	headers := 0
	dataRows := 0
	for _, item := range items {
		if item.isHeader {
			headers++
		} else {
			dataRows++
		}
	}
	if headers != 3 {
		t.Errorf("headers = %d, want 3 (firing, ack, resolved)", headers)
	}
	if dataRows != 4 {
		t.Errorf("data rows = %d, want 4", dataRows)
	}

	// Verify order: first header is "FIRING".
	if !strings.Contains(items[0].header, "FIRING") {
		t.Errorf("first header = %q, want FIRING", items[0].header)
	}
}

func TestAlertViewCursorSkipsHeaders(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewAlerts
	s.Alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "a", Severity: "critical", FiredAt: 100},                    // firing
		{ID: 2, RuleName: "b", Severity: "warning", FiredAt: 200, Acknowledged: true}, // acked
	}

	items := buildSectionItems(s.Alertv.alerts, false)
	// Items: FIRING header (0), a (1), ACK header (2), b (3)

	// Start on first data row.
	clampCursorToItems(&s.Alertv, items)
	if s.Alertv.cursor != 1 {
		t.Fatalf("initial cursor = %d, want 1", s.Alertv.cursor)
	}

	// Move down — should skip header at index 2, land on index 3.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if s.Alertv.cursor != 3 {
		t.Errorf("cursor after j = %d, want 3 (should skip header)", s.Alertv.cursor)
	}

	// Move up — should skip header at index 2, land on index 1.
	updateAlertView(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if s.Alertv.cursor != 1 {
		t.Errorf("cursor after k = %d, want 1 (should skip header)", s.Alertv.cursor)
	}
}

func TestAlertViewRenderSections(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Alertv.alerts = []protocol.AlertMsg{
		{ID: 1, RuleName: "a", Severity: "critical", FiredAt: 100, Message: "cpu"},
		{ID: 2, RuleName: "b", Severity: "warning", FiredAt: 200, Acknowledged: true, Message: "disk"},
		{ID: 3, RuleName: "c", Severity: "warning", FiredAt: 300, ResolvedAt: 400, Message: "mem"},
	}

	got := renderAlertView(&a, s, 100, 20)
	plain := stripANSI(got)

	if !strings.Contains(plain, "FIRING (1)") {
		t.Error("should show FIRING section header")
	}
	if !strings.Contains(plain, "ACKNOWLEDGED (1)") {
		t.Error("should show ACKNOWLEDGED section header")
	}
	if !strings.Contains(plain, "RESOLVED (1)") {
		t.Error("should show RESOLVED section header")
	}
	if !strings.Contains(plain, "Alerts") {
		t.Error("should show Alerts panel title")
	}
	if !strings.Contains(plain, "Rules") {
		t.Error("should show Rules panel title")
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		ts   int64
		want string
	}{
		{now.Add(-30 * time.Second).Unix(), "30s ago"},
		{now.Add(-5 * time.Minute).Unix(), "5m ago"},
		{now.Add(-3 * time.Hour).Unix(), "3h ago"},
		{now.Add(-48 * time.Hour).Unix(), "2d ago"},
		{0, ""},
	}
	for _, tt := range tests {
		got := relativeTime(now, tt.ts)
		if got != tt.want {
			t.Errorf("relativeTime(%d) = %q, want %q", tt.ts, got, tt.want)
		}
	}
}

func TestAlertViewRulesSubView(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewAlerts
	s.Alertv.subView = 1
	s.Alertv.rules = []protocol.AlertRuleInfo{
		{Name: "high_cpu", Condition: "host.cpu_percent > 90", Severity: "critical", FiringCount: 2},
		{Name: "exited", Condition: "container.state == 'exited'", Severity: "warning", FiringCount: 0},
	}

	got := renderAlertView(&a, s, 100, 20)
	plain := stripANSI(got)

	if !strings.Contains(plain, "high_cpu") {
		t.Error("should show high_cpu rule")
	}
	if !strings.Contains(plain, "2 firing") {
		t.Error("should show firing count for high_cpu")
	}
	if !strings.Contains(plain, "ok") {
		t.Error("should show ok status for exited rule")
	}
	if !strings.Contains(plain, "Alerts") {
		t.Error("should show Alerts panel title")
	}
	if !strings.Contains(plain, "Rules") {
		t.Error("should show Rules panel title")
	}
}

func TestAlertViewRulesQueryMsg(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Alertv.rulesStale = true

	rules := []protocol.AlertRuleInfo{
		{Name: "test", Condition: "host.cpu_percent > 80", Severity: "warning"},
	}
	model, _ := a.Update(alertRulesQueryMsg{rules: rules})
	a = model.(App)

	s = a.session()
	if s.Alertv.rulesStale {
		t.Error("rulesStale should be false after query result")
	}
	if len(s.Alertv.rules) != 1 {
		t.Errorf("rules = %d, want 1", len(s.Alertv.rules))
	}
}

func TestAlertActionDoneSetsRulesStale(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Alertv.rulesStale = false

	model, _ := a.Update(alertActionDoneMsg{})
	a = model.(App)

	s = a.session()
	if !s.Alertv.rulesStale {
		t.Error("alertActionDoneMsg should set rulesStale = true")
	}
}

func TestFormatDurationShort(t *testing.T) {
	tests := []struct {
		secs int64
		want string
	}{
		{0, ""},
		{300, "5m"},
		{3600, "1h"},
		{7200, "2h"},
		{86400, "1d"},
		{172800, "2d"},
	}
	for _, tt := range tests {
		got := formatDurationShort(tt.secs)
		if got != tt.want {
			t.Errorf("formatDurationShort(%d) = %q, want %q", tt.secs, got, tt.want)
		}
	}
}
