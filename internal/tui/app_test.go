package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/thobiasn/rook/internal/protocol"
)

// newTestApp creates a minimal App for testing (no real client).
func newTestApp() App {
	a := App{
		theme:          DefaultTheme(),
		logs:           NewRingBuffer[protocol.LogEntryMsg](500),
		alerts:         make(map[int64]*protocol.AlertEvent),
		rates:          NewRateCalc(),
		cpuHistory:     make(map[string]*RingBuffer[float64]),
		memHistory:     make(map[string]*RingBuffer[float64]),
		hostCPUHistory: NewRingBuffer[float64](180),
		hostMemHistory: NewRingBuffer[float64](180),
		dash:           newDashboardState(),
		logv:           newLogViewState(),
		alertv:         newAlertViewState(),
		width:          120,
		height:         40,
	}
	return a
}

func TestAppUpdateMetricsAccumulates(t *testing.T) {
	a := newTestApp()
	m := &protocol.MetricsUpdate{
		Timestamp: 100,
		Host:      &protocol.HostMetrics{CPUPercent: 42.5, MemPercent: 60.0},
		Containers: []protocol.ContainerMetrics{
			{ID: "c1", Name: "web", State: "running", CPUPercent: 10.0, MemPercent: 50.0},
		},
	}
	model, _ := a.Update(MetricsMsg{m})
	a = model.(App)

	if a.host == nil || a.host.CPUPercent != 42.5 {
		t.Error("host metrics not accumulated")
	}
	if len(a.containers) != 1 || a.containers[0].ID != "c1" {
		t.Error("container metrics not accumulated")
	}
	if a.hostCPUHistory.Len() != 1 {
		t.Errorf("hostCPUHistory.Len() = %d, want 1", a.hostCPUHistory.Len())
	}
	if _, ok := a.cpuHistory["c1"]; !ok {
		t.Error("container CPU history not created")
	}
}

func TestAppUpdateMetricsStaleCleanup(t *testing.T) {
	a := newTestApp()
	// First update with container c1.
	m1 := &protocol.MetricsUpdate{
		Timestamp:  100,
		Containers: []protocol.ContainerMetrics{{ID: "c1", CPUPercent: 5}},
	}
	model, _ := a.Update(MetricsMsg{m1})
	a = model.(App)

	if _, ok := a.cpuHistory["c1"]; !ok {
		t.Fatal("c1 history should exist")
	}

	// Second update without c1 — should clean up.
	m2 := &protocol.MetricsUpdate{
		Timestamp:  110,
		Containers: []protocol.ContainerMetrics{{ID: "c2", CPUPercent: 10}},
	}
	model, _ = a.Update(MetricsMsg{m2})
	a = model.(App)

	if _, ok := a.cpuHistory["c1"]; ok {
		t.Error("stale c1 history should be cleaned up")
	}
	if _, ok := a.cpuHistory["c2"]; !ok {
		t.Error("c2 history should exist")
	}
}

func TestAppUpdateLogRoutesToAllViews(t *testing.T) {
	a := newTestApp()
	a.active = viewDashboard // Not on log view or detail view.
	a.detail.containerID = "c1"
	a.detail.reset()

	entry := protocol.LogEntryMsg{Timestamp: 100, ContainerID: "c1", Message: "hello"}
	model, _ := a.Update(LogMsg{entry})
	a = model.(App)

	// Dashboard log buffer should have the entry.
	if a.logs.Len() != 1 {
		t.Errorf("dashboard logs.Len() = %d, want 1", a.logs.Len())
	}
	// Full-screen log view should have the entry.
	if a.logv.logs.Len() != 1 {
		t.Errorf("logv.logs.Len() = %d, want 1", a.logv.logs.Len())
	}
	// Detail view should have the entry (matches container).
	if a.detail.logs.Len() != 1 {
		t.Errorf("detail.logs.Len() = %d, want 1", a.detail.logs.Len())
	}
}

func TestAppUpdateLogDetailFilters(t *testing.T) {
	a := newTestApp()
	a.detail.containerID = "c1"
	a.detail.reset()

	// Entry for different container should not appear in detail.
	entry := protocol.LogEntryMsg{Timestamp: 100, ContainerID: "c2", Message: "other"}
	model, _ := a.Update(LogMsg{entry})
	a = model.(App)

	if a.detail.logs.Len() != 0 {
		t.Errorf("detail should filter non-matching container, got %d entries", a.detail.logs.Len())
	}
	// But logv should still have it.
	if a.logv.logs.Len() != 1 {
		t.Errorf("logv should have entry, got %d", a.logv.logs.Len())
	}
}

func TestAppUpdateAlertAddDelete(t *testing.T) {
	a := newTestApp()

	// Fire an alert.
	evt := protocol.AlertEvent{ID: 1, RuleName: "test", State: "firing", FiredAt: 100}
	model, _ := a.Update(AlertEventMsg{evt})
	a = model.(App)

	if len(a.alerts) != 1 {
		t.Fatalf("alerts count = %d, want 1", len(a.alerts))
	}

	// Resolve the alert.
	resolved := protocol.AlertEvent{ID: 1, State: "resolved"}
	model, _ = a.Update(AlertEventMsg{resolved})
	a = model.(App)

	if len(a.alerts) != 0 {
		t.Errorf("alerts count after resolve = %d, want 0", len(a.alerts))
	}
}

func TestAppUpdateAlertMapCapped(t *testing.T) {
	a := newTestApp()

	// Fill up to cap.
	for i := int64(0); i < 1001; i++ {
		evt := protocol.AlertEvent{ID: i, RuleName: "test", State: "firing", FiredAt: i}
		model, _ := a.Update(AlertEventMsg{evt})
		a = model.(App)
	}

	if len(a.alerts) > 1000 {
		t.Errorf("alerts count = %d, should be capped at 1000", len(a.alerts))
	}
}

func TestAppUpdateAlertActionDone(t *testing.T) {
	a := newTestApp()
	a.alertv.stale = false

	model, _ := a.Update(alertActionDoneMsg{})
	a = model.(App)

	if !a.alertv.stale {
		t.Error("alertv should be marked stale after action done")
	}
}

func TestAppViewSwitchTab(t *testing.T) {
	a := newTestApp()

	if a.active != viewDashboard {
		t.Fatal("should start on dashboard")
	}

	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	a = model.(App)
	if a.active != viewDetail {
		t.Errorf("after '2', active = %d, want viewDetail", a.active)
	}

	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	a = model.(App)
	if a.active != viewAlerts {
		t.Errorf("after '3', active = %d, want viewAlerts", a.active)
	}

	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	a = model.(App)
	if a.active != viewDashboard {
		t.Errorf("after '1', active = %d, want viewDashboard", a.active)
	}
}

func TestAppHelpToggle(t *testing.T) {
	a := newTestApp()
	if a.showHelp {
		t.Fatal("help should be off initially")
	}

	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	a = model.(App)
	if !a.showHelp {
		t.Error("help should be on after ?")
	}

	// Any key dismisses help.
	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	a = model.(App)
	if a.showHelp {
		t.Error("help should be off after any key")
	}
}

func TestAppWindowSize(t *testing.T) {
	a := newTestApp()
	model, _ := a.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	a = model.(App)

	if a.width != 200 || a.height != 50 {
		t.Errorf("size = %dx%d, want 200x50", a.width, a.height)
	}
}

func TestAppViewRendersWithoutPanic(t *testing.T) {
	a := newTestApp()

	// Connecting state.
	a.width = 0
	a.height = 0
	v := a.View()
	if v != "Connecting..." {
		t.Errorf("zero size should show connecting, got %q", v)
	}

	// Error state.
	a.err = tea.ErrProgramKilled
	v = a.View()
	if v == "" {
		t.Error("error view should not be empty")
	}

	// Normal state - all views.
	a.err = nil
	a.width = 120
	a.height = 40
	for _, view := range []view{viewDashboard, viewLogs, viewAlerts, viewDetail} {
		a.active = view
		v := a.View()
		if v == "" {
			t.Errorf("view %d rendered empty", view)
		}
	}
}
