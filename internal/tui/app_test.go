package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/thobiasn/rook/internal/protocol"
)

const testServer = "test"

// newTestSession creates a minimal Session for testing (no real client).
func newTestSession() *Session {
	return NewSession(testServer, nil, nil)
}

// newTestApp creates a minimal App for testing (no real client).
func newTestApp() App {
	s := newTestSession()
	return App{
		sessions:      map[string]*Session{testServer: s},
		sessionOrder:  []string{testServer},
		activeSession: testServer,
		theme:         DefaultTheme(),
		width:         120,
		height:        40,
	}
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
	model, _ := a.Update(MetricsMsg{m, testServer})
	a = model.(App)

	s := a.session()
	if s.Host == nil || s.Host.CPUPercent != 42.5 {
		t.Error("host metrics not accumulated")
	}
	if len(s.Containers) != 1 || s.Containers[0].ID != "c1" {
		t.Error("container metrics not accumulated")
	}
	if s.HostCPUHistory.Len() != 1 {
		t.Errorf("HostCPUHistory.Len() = %d, want 1", s.HostCPUHistory.Len())
	}
	if _, ok := s.CPUHistory["c1"]; !ok {
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
	model, _ := a.Update(MetricsMsg{m1, testServer})
	a = model.(App)

	s := a.session()
	if _, ok := s.CPUHistory["c1"]; !ok {
		t.Fatal("c1 history should exist")
	}

	// Second update without c1 — should clean up.
	m2 := &protocol.MetricsUpdate{
		Timestamp:  110,
		Containers: []protocol.ContainerMetrics{{ID: "c2", CPUPercent: 10}},
	}
	model, _ = a.Update(MetricsMsg{m2, testServer})
	a = model.(App)

	s = a.session()
	if _, ok := s.CPUHistory["c1"]; ok {
		t.Error("stale c1 history should be cleaned up")
	}
	if _, ok := s.CPUHistory["c2"]; !ok {
		t.Error("c2 history should exist")
	}
}

func TestAppUpdateLogRoutesToDetail(t *testing.T) {
	a := newTestApp()
	a.active = viewDashboard
	s := a.session()
	s.Detail.containerID = "c1"
	s.Detail.reset()

	entry := protocol.LogEntryMsg{Timestamp: 100, ContainerID: "c1", Message: "hello"}
	model, _ := a.Update(LogMsg{entry, testServer})
	a = model.(App)

	s = a.session()
	if s.Detail.logs.Len() != 1 {
		t.Errorf("detail.logs.Len() = %d, want 1", s.Detail.logs.Len())
	}
}

func TestAppUpdateLogDetailFilters(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Detail.containerID = "c1"
	s.Detail.reset()

	// Entry for different container should not appear in detail.
	entry := protocol.LogEntryMsg{Timestamp: 100, ContainerID: "c2", Message: "other"}
	model, _ := a.Update(LogMsg{entry, testServer})
	a = model.(App)

	s = a.session()
	if s.Detail.logs.Len() != 0 {
		t.Errorf("detail should filter non-matching container, got %d entries", s.Detail.logs.Len())
	}
}

func TestAppUpdateAlertAddDelete(t *testing.T) {
	a := newTestApp()

	// Fire an alert.
	evt := protocol.AlertEvent{ID: 1, RuleName: "test", State: "firing", FiredAt: 100}
	model, _ := a.Update(AlertEventMsg{evt, testServer})
	a = model.(App)

	s := a.session()
	if len(s.Alerts) != 1 {
		t.Fatalf("alerts count = %d, want 1", len(s.Alerts))
	}

	// Resolve the alert.
	resolved := protocol.AlertEvent{ID: 1, State: "resolved"}
	model, _ = a.Update(AlertEventMsg{resolved, testServer})
	a = model.(App)

	s = a.session()
	if len(s.Alerts) != 0 {
		t.Errorf("alerts count after resolve = %d, want 0", len(s.Alerts))
	}
}

func TestAppUpdateAlertMapCapped(t *testing.T) {
	a := newTestApp()

	for i := int64(0); i < 1001; i++ {
		evt := protocol.AlertEvent{ID: i, RuleName: "test", State: "firing", FiredAt: i}
		model, _ := a.Update(AlertEventMsg{evt, testServer})
		a = model.(App)
	}

	s := a.session()
	if len(s.Alerts) > 1000 {
		t.Errorf("alerts count = %d, should be capped at 1000", len(s.Alerts))
	}
}

func TestAppUpdateAlertActionDone(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Alertv.stale = false

	model, _ := a.Update(alertActionDoneMsg{})
	a = model.(App)

	s = a.session()
	if !s.Alertv.stale {
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
	if a.active != viewAlerts {
		t.Errorf("after '2', active = %d, want viewAlerts", a.active)
	}

	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	a = model.(App)
	if a.active != viewDashboard {
		t.Errorf("after '1', active = %d, want viewDashboard", a.active)
	}

	// Tab should cycle dashboard <-> alerts.
	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyTab})
	a = model.(App)
	if a.active != viewAlerts {
		t.Errorf("tab from dashboard should go to alerts, got %d", a.active)
	}

	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyTab})
	a = model.(App)
	if a.active != viewDashboard {
		t.Errorf("tab from alerts should go to dashboard, got %d", a.active)
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
	for _, view := range []view{viewDashboard, viewAlerts, viewDetail} {
		a.active = view
		v := a.View()
		if v == "" {
			t.Errorf("view %d rendered empty", view)
		}
	}
}

func TestAppServerPickerToggle(t *testing.T) {
	// Multi-server setup.
	s1 := NewSession("prod", nil, nil)
	s2 := NewSession("staging", nil, nil)
	a := NewApp(map[string]*Session{"prod": s1, "staging": s2})
	a.width = 120
	a.height = 40

	// S opens picker.
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	a = model.(App)
	if !a.showServerPicker {
		t.Error("S should open server picker")
	}

	// "2" selects second server.
	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	a = model.(App)
	if a.showServerPicker {
		t.Error("picker should close after selection")
	}
	if a.activeSession != "staging" {
		t.Errorf("activeSession = %q, want staging", a.activeSession)
	}
}

func TestAppServerPickerSingleServer(t *testing.T) {
	a := newTestApp()

	// S should NOT open picker when single server.
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	a = model.(App)
	if a.showServerPicker {
		t.Error("S should not open picker for single server")
	}
}

func TestHandleMetricsBackfill(t *testing.T) {
	s := newTestSession()
	resp := &protocol.QueryMetricsResp{
		Host: []protocol.TimedHostMetrics{
			{Timestamp: 1, HostMetrics: protocol.HostMetrics{CPUPercent: 10, MemPercent: 20, MemTotal: 1000, MemFree: 300, MemCached: 200}},
			{Timestamp: 2, HostMetrics: protocol.HostMetrics{CPUPercent: 30, MemPercent: 40, MemTotal: 1000, MemFree: 250, MemCached: 150}},
			{Timestamp: 3, HostMetrics: protocol.HostMetrics{CPUPercent: 50, MemPercent: 60, MemTotal: 1000, MemFree: 200, MemCached: 100}},
		},
		Containers: []protocol.TimedContainerMetrics{
			{Timestamp: 1, ContainerMetrics: protocol.ContainerMetrics{ID: "c1", CPUPercent: 5, MemPercent: 15}},
			{Timestamp: 2, ContainerMetrics: protocol.ContainerMetrics{ID: "c1", CPUPercent: 25, MemPercent: 35}},
			{Timestamp: 1, ContainerMetrics: protocol.ContainerMetrics{ID: "c2", CPUPercent: 8, MemPercent: 18}},
		},
	}
	handleMetricsBackfill(s, resp, 0, 0, false)

	// Host history populated in order.
	if s.HostCPUHistory.Len() != 3 {
		t.Errorf("HostCPUHistory.Len() = %d, want 3", s.HostCPUHistory.Len())
	}
	cpuData := s.HostCPUHistory.Data()
	if cpuData[0] != 10 || cpuData[2] != 50 {
		t.Errorf("HostCPUHistory = %v, want [10 30 50]", cpuData)
	}
	if s.HostMemHistory.Len() != 3 {
		t.Errorf("HostMemHistory.Len() = %d, want 3", s.HostMemHistory.Len())
	}

	// Memory usage history populated.
	if s.HostMemUsedHistory.Len() != 3 {
		t.Errorf("HostMemUsedHistory.Len() = %d, want 3", s.HostMemUsedHistory.Len())
	}
	// Container histories created and populated.
	if s.CPUHistory["c1"].Len() != 2 {
		t.Errorf("c1 CPUHistory.Len() = %d, want 2", s.CPUHistory["c1"].Len())
	}
	if s.CPUHistory["c2"].Len() != 1 {
		t.Errorf("c2 CPUHistory.Len() = %d, want 1", s.CPUHistory["c2"].Len())
	}
	if s.MemHistory["c1"].Len() != 2 {
		t.Errorf("c1 MemHistory.Len() = %d, want 2", s.MemHistory["c1"].Len())
	}
}

func TestHandleMetricsBackfillTimeAlign(t *testing.T) {
	s := newTestSession()
	// Simulate a 10-second window with 5 buckets. Data only in the last 2 seconds.
	resp := &protocol.QueryMetricsResp{
		Containers: []protocol.TimedContainerMetrics{
			{Timestamp: 8, ContainerMetrics: protocol.ContainerMetrics{ID: "c1", CPUPercent: 10, MemUsage: 100}},
			{Timestamp: 9, ContainerMetrics: protocol.ContainerMetrics{ID: "c1", CPUPercent: 20, MemUsage: 200}},
		},
	}
	handleMetricsBackfill(s, resp, 0, 10, true)

	// With rangeHist=true and sparse data, the TUI should produce
	// ringBufSize entries with zero-fill for empty buckets.
	if s.CPUHistory["c1"].Len() != ringBufSize {
		t.Fatalf("c1 CPUHistory.Len() = %d, want %d", s.CPUHistory["c1"].Len(), ringBufSize)
	}
	cpu := s.CPUHistory["c1"].Data()
	// Data at ts 8,9 in a 0-10 window with 600 buckets:
	// bucket duration = 10/600 = 0.01667s per bucket
	// ts=8 → bucket 480, ts=9 → bucket 540
	// Most buckets should be zero.
	var nonZero int
	for _, v := range cpu {
		if v > 0 {
			nonZero++
		}
	}
	if nonZero != 2 {
		t.Errorf("nonzero buckets = %d, want 2", nonZero)
	}
}

func TestAppUpdateMetricsBackfillMsg(t *testing.T) {
	a := newTestApp()
	resp := &protocol.QueryMetricsResp{
		Host: []protocol.TimedHostMetrics{
			{Timestamp: 1, HostMetrics: protocol.HostMetrics{CPUPercent: 42, MemPercent: 55}},
		},
	}
	model, _ := a.Update(metricsBackfillMsg{server: testServer, resp: resp})
	a = model.(App)

	s := a.session()
	if s.HostCPUHistory.Len() != 1 {
		t.Errorf("HostCPUHistory.Len() = %d, want 1", s.HostCPUHistory.Len())
	}

	// Nil resp should be a no-op.
	model, _ = a.Update(metricsBackfillMsg{server: testServer, resp: nil})
	a = model.(App)
	s = a.session()
	if s.HostCPUHistory.Len() != 1 {
		t.Errorf("nil resp should be no-op, HostCPUHistory.Len() = %d, want 1", s.HostCPUHistory.Len())
	}

	// Unknown server should be a no-op.
	model, _ = a.Update(metricsBackfillMsg{server: "unknown", resp: resp})
	a = model.(App)
}

func TestAppMultiServerMessageRouting(t *testing.T) {
	s1 := NewSession("prod", nil, nil)
	s2 := NewSession("staging", nil, nil)
	a := NewApp(map[string]*Session{"prod": s1, "staging": s2})
	a.width = 120
	a.height = 40

	// Metrics for prod should only go to prod.
	m := &protocol.MetricsUpdate{
		Timestamp: 100,
		Host:      &protocol.HostMetrics{CPUPercent: 42.0},
	}
	model, _ := a.Update(MetricsMsg{m, "prod"})
	a = model.(App)

	if a.sessions["prod"].Host == nil || a.sessions["prod"].Host.CPUPercent != 42.0 {
		t.Error("prod should have received metrics")
	}
	if a.sessions["staging"].Host != nil {
		t.Error("staging should not have received prod's metrics")
	}
}
