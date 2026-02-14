package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/thobiasn/tori-cli/internal/protocol"
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
		ctx:           &appCtx{},
	}
}

// newTestAppWithDisplay creates a test app with date/time display config.
func newTestAppWithDisplay() App {
	a := newTestApp()
	a.displayCfg = DisplayConfig{DateFormat: "2006-01-02", TimeFormat: "15:04:05"}
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

	// Tab on dashboard toggles focus between servers and containers.
	// Default is focusServers, so first tab goes to focusContainers.
	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyTab})
	a = model.(App)
	if a.active != viewDashboard {
		t.Errorf("tab on dashboard should stay on dashboard, got %d", a.active)
	}
	if a.dashFocus != focusContainers {
		t.Errorf("tab should toggle to focusContainers, got %d", a.dashFocus)
	}

	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyTab})
	a = model.(App)
	if a.dashFocus != focusServers {
		t.Errorf("tab should toggle back to focusServers, got %d", a.dashFocus)
	}

	// Tab from alerts toggles sub-view (alerts/rules), not back to dashboard.
	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	a = model.(App)
	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyTab})
	a = model.(App)
	if a.active != viewAlerts {
		t.Errorf("tab from alerts should stay on alerts (sub-view toggle), got %d", a.active)
	}
	if a.session().Alertv.subView != 1 {
		t.Errorf("tab should switch to rules sub-view, got %d", a.session().Alertv.subView)
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

	// Connecting state (with animated bird spinner).
	a.width = 0
	a.height = 0
	v := a.View()
	if !strings.Contains(v, "Connecting...") {
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

func TestHandleMetricsBackfill(t *testing.T) {
	s := newTestSession()
	// ContInfo maps service keys to current container IDs.
	s.ContInfo = []protocol.ContainerInfo{
		{ID: "c1", Name: "web", Project: "app", Service: "web"},
		{ID: "c2", Name: "api", Project: "app", Service: "api"},
	}
	resp := &protocol.QueryMetricsResp{
		Host: []protocol.TimedHostMetrics{
			{Timestamp: 1, HostMetrics: protocol.HostMetrics{CPUPercent: 10, MemPercent: 20, MemTotal: 1000, MemFree: 300, MemCached: 200}},
			{Timestamp: 2, HostMetrics: protocol.HostMetrics{CPUPercent: 30, MemPercent: 40, MemTotal: 1000, MemFree: 250, MemCached: 150}},
			{Timestamp: 3, HostMetrics: protocol.HostMetrics{CPUPercent: 50, MemPercent: 60, MemTotal: 1000, MemFree: 200, MemCached: 100}},
		},
		Containers: []protocol.TimedContainerMetrics{
			{Timestamp: 1, ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "web", CPUPercent: 5, MemUsage: 1500}},
			{Timestamp: 2, ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "web", CPUPercent: 25, MemUsage: 3500}},
			{Timestamp: 1, ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "api", CPUPercent: 8, MemUsage: 1800}},
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
	// Container histories mapped by container ID via ContInfo.
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

func TestHandleMetricsBackfillHistorical(t *testing.T) {
	s := newTestSession()
	s.ContInfo = []protocol.ContainerInfo{
		{ID: "c1", Name: "web", Project: "app", Service: "web"},
	}
	// Pre-populate with some existing data.
	s.HostCPUHistory.Push(99)
	s.CPUHistory["old"] = NewRingBuffer[float64](ringBufSize)
	s.CPUHistory["old"].Push(50)
	s.MemHistory["old"] = NewRingBuffer[float64](ringBufSize)

	// Historical backfill: agent sends pre-bucketed data (e.g. 2 points).
	resp := &protocol.QueryMetricsResp{
		Host: []protocol.TimedHostMetrics{
			{Timestamp: 1, HostMetrics: protocol.HostMetrics{CPUPercent: 10, MemPercent: 20}},
			{Timestamp: 2, HostMetrics: protocol.HostMetrics{CPUPercent: 30, MemPercent: 40}},
		},
		Containers: []protocol.TimedContainerMetrics{
			{Timestamp: 1, ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "web", CPUPercent: 5}},
			{Timestamp: 2, ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "web", CPUPercent: 15}},
		},
	}
	handleMetricsBackfill(s, resp, 0, 10, true)

	// Host buffers should be replaced atomically (not appended).
	if s.HostCPUHistory.Len() != 2 {
		t.Errorf("HostCPUHistory.Len() = %d, want 2 (replaced)", s.HostCPUHistory.Len())
	}
	cpu := s.HostCPUHistory.Data()
	if cpu[0] != 10 || cpu[1] != 30 {
		t.Errorf("HostCPUHistory = %v, want [10 30]", cpu)
	}

	// Container buffers should be replaced, mapped to c1 via ContInfo.
	if s.CPUHistory["c1"].Len() != 2 {
		t.Errorf("c1 CPUHistory.Len() = %d, want 2", s.CPUHistory["c1"].Len())
	}
}

func TestGlobalBackfillSkipsDetailContainer(t *testing.T) {
	s := newTestSession()
	s.ContInfo = []protocol.ContainerInfo{
		{ID: "c1", Name: "web", Project: "app", Service: "web"},
		{ID: "c2", Name: "api", Project: "app", Service: "api"},
	}
	// Simulate detail view pending for container "c1".
	s.Detail.containerID = "c1"
	s.Detail.svcService = "web"
	s.Detail.metricsBackfillPending = true

	resp := &protocol.QueryMetricsResp{
		Containers: []protocol.TimedContainerMetrics{
			{Timestamp: 1, ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "web", CPUPercent: 10}},
			{Timestamp: 1, ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "api", CPUPercent: 20}},
		},
	}
	handleMetricsBackfill(s, resp, 0, 0, false)

	// c1 should be skipped (detail backfill handles it).
	if _, ok := s.CPUHistory["c1"]; ok {
		t.Error("c1 should be skipped when detail backfill is pending")
	}
	// c2 should be populated normally.
	if s.CPUHistory["c2"].Len() != 1 {
		t.Errorf("c2 CPUHistory.Len() = %d, want 1", s.CPUHistory["c2"].Len())
	}
}

func TestHandleDetailMetricsBackfill(t *testing.T) {
	s := newTestSession()
	s.ContInfo = []protocol.ContainerInfo{
		{ID: "new-c", Name: "web", Project: "app", Service: "web"},
	}
	det := &s.Detail
	det.containerID = "new-c"
	det.reset()

	// Agent sends service-scoped data keyed by (project, service).
	resp := &protocol.QueryMetricsResp{
		Containers: []protocol.TimedContainerMetrics{
			{Timestamp: 100, ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "web", CPUPercent: 10, MemUsage: 1000}},
			{Timestamp: 200, ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "web", CPUPercent: 20, MemUsage: 2000}},
			{Timestamp: 300, ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "web", CPUPercent: 30, MemUsage: 3000}},
			{Timestamp: 400, ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "web", CPUPercent: 40, MemUsage: 4000}},
		},
	}
	handleDetailMetricsBackfill(s, det, resp, 0, 0, 0)

	// metricsBackfilled should be set.
	if !det.metricsBackfilled {
		t.Error("metricsBackfilled should be true")
	}

	// All 4 data points should be in the buffer, mapped to new-c via ContInfo.
	cpuBuf, ok := s.CPUHistory["new-c"]
	if !ok {
		t.Fatal("CPUHistory['new-c'] should exist")
	}
	cpuData := cpuBuf.Data()
	if len(cpuData) != 4 {
		t.Errorf("CPUHistory['new-c'].Len() = %d, want 4", len(cpuData))
	}
	if cpuData[0] != 10 || cpuData[1] != 20 || cpuData[2] != 30 || cpuData[3] != 40 {
		t.Errorf("CPU data = %v, want [10 20 30 40]", cpuData)
	}

	memBuf, ok := s.MemHistory["new-c"]
	if !ok {
		t.Fatal("MemHistory['new-c'] should exist")
	}
	memData := memBuf.Data()
	if len(memData) != 4 {
		t.Errorf("MemHistory['new-c'].Len() = %d, want 4", len(memData))
	}
}

func TestHandleDetailMetricsBackfillHistorical(t *testing.T) {
	s := newTestSession()
	s.ContInfo = []protocol.ContainerInfo{
		{ID: "c1", Name: "web", Project: "app", Service: "web"},
	}
	det := &s.Detail
	det.containerID = "c1"
	det.reset()

	// Agent sends pre-bucketed historical data (e.g. 3 points with zero-fill).
	resp := &protocol.QueryMetricsResp{
		Containers: []protocol.TimedContainerMetrics{
			{Timestamp: 2, ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "web", CPUPercent: 0, MemUsage: 0}},
			{Timestamp: 5, ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "web", CPUPercent: 10, MemUsage: 100}},
			{Timestamp: 8, ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "web", CPUPercent: 20, MemUsage: 200}},
		},
	}
	handleDetailMetricsBackfill(s, det, resp, 0, 10, 10)

	cpuBuf := s.CPUHistory["c1"]
	if cpuBuf == nil {
		t.Fatal("CPUHistory['c1'] should exist")
	}
	if cpuBuf.Len() != 3 {
		t.Errorf("CPUHistory.Len() = %d, want 3", cpuBuf.Len())
	}
}

func TestHandleDetailMetricsBackfillGroupMode(t *testing.T) {
	s := newTestSession()
	s.ContInfo = []protocol.ContainerInfo{
		{ID: "c1", Name: "web", Project: "myapp", Service: "web"},
		{ID: "c2", Name: "api", Project: "myapp", Service: "api"},
	}
	det := &s.Detail
	det.project = "myapp"
	det.containerID = "" // group mode
	det.reset()

	// Agent sends data for two services in the group.
	resp := &protocol.QueryMetricsResp{
		Containers: []protocol.TimedContainerMetrics{
			{Timestamp: 1, ContainerMetrics: protocol.ContainerMetrics{Project: "myapp", Service: "web", CPUPercent: 10, MemUsage: 100}},
			{Timestamp: 1, ContainerMetrics: protocol.ContainerMetrics{Project: "myapp", Service: "api", CPUPercent: 20, MemUsage: 200}},
			{Timestamp: 2, ContainerMetrics: protocol.ContainerMetrics{Project: "myapp", Service: "web", CPUPercent: 15, MemUsage: 150}},
		},
	}
	handleDetailMetricsBackfill(s, det, resp, 0, 0, 0)

	if !det.metricsBackfilled {
		t.Error("metricsBackfilled should be true")
	}
	if s.CPUHistory["c1"].Len() != 2 {
		t.Errorf("c1 CPUHistory.Len() = %d, want 2", s.CPUHistory["c1"].Len())
	}
	if s.CPUHistory["c2"].Len() != 1 {
		t.Errorf("c2 CPUHistory.Len() = %d, want 1", s.CPUHistory["c2"].Len())
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
	a := NewApp(map[string]*Session{"prod": s1, "staging": s2}, DisplayConfig{DateFormat: "2006-01-02", TimeFormat: "15:04:05"})
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

func TestHandleDetailAutoSwitch(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewDetail

	// Viewing container "old-c" with a compose service identity.
	s.Detail.containerID = "old-c"
	s.Detail.svcProject = "myapp"
	s.Detail.svcService = "web"
	s.Detail.reset()

	// A new container starts with the same service identity.
	evt := protocol.ContainerEvent{
		ContainerID: "new-c",
		Name:        "web",
		State:       "running",
		Action:      "start",
		Project:     "myapp",
		Service:     "web",
	}
	cmd := a.handleDetailAutoSwitch(s, evt)

	// Should have switched to the new container.
	if s.Detail.containerID != "new-c" {
		t.Errorf("containerID = %q, want new-c", s.Detail.containerID)
	}
	// Should return a non-nil cmd (onSwitch triggers backfills).
	if cmd == nil {
		t.Error("expected non-nil cmd from auto-switch")
	}
	// Backfill flags should be reset.
	if s.Detail.backfilled {
		t.Error("backfilled should be false after reset")
	}
	if s.Detail.metricsBackfilled {
		t.Error("metricsBackfilled should be false after reset")
	}
}

func TestHandleDetailAutoSwitchNoMatch(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewDetail

	s.Detail.containerID = "old-c"
	s.Detail.svcProject = "myapp"
	s.Detail.svcService = "web"
	s.Detail.reset()

	// Different service — should NOT auto-switch.
	evt := protocol.ContainerEvent{
		ContainerID: "new-c",
		Name:        "api",
		State:       "running",
		Action:      "start",
		Project:     "myapp",
		Service:     "api",
	}
	cmd := a.handleDetailAutoSwitch(s, evt)

	if s.Detail.containerID != "old-c" {
		t.Errorf("containerID = %q, should still be old-c", s.Detail.containerID)
	}
	if cmd != nil {
		t.Error("should return nil cmd for non-matching service")
	}
}

func TestHandleDetailAutoSwitchNonCompose(t *testing.T) {
	a := newTestApp()
	s := a.session()
	a.active = viewDetail

	// Non-compose container: svcProject="" svcService="myapp"
	s.Detail.containerID = "old-c"
	s.Detail.svcProject = ""
	s.Detail.svcService = "myapp"
	s.Detail.reset()

	// New container with same name, no compose labels.
	evt := protocol.ContainerEvent{
		ContainerID: "new-c",
		Name:        "myapp",
		State:       "running",
		Action:      "start",
	}
	cmd := a.handleDetailAutoSwitch(s, evt)

	if s.Detail.containerID != "new-c" {
		t.Errorf("containerID = %q, want new-c", s.Detail.containerID)
	}
	if cmd == nil {
		t.Error("expected non-nil cmd for non-compose auto-switch")
	}
}

func TestStreamingBufferTransfer(t *testing.T) {
	a := newTestApp()
	s := a.session()

	// Set up ContInfo with old container.
	s.ContInfo = []protocol.ContainerInfo{
		{ID: "old-c", Name: "web", Project: "myapp", Service: "web", State: "exited"},
	}

	// Push some history for old container.
	s.CPUHistory["old-c"] = NewRingBuffer[float64](ringBufSize)
	s.MemHistory["old-c"] = NewRingBuffer[float64](ringBufSize)
	for i := 0; i < 5; i++ {
		s.CPUHistory["old-c"].Push(float64(i * 10))
		s.MemHistory["old-c"].Push(float64(i * 100))
	}

	// Streaming update: new container with same service identity replaces old.
	m := &protocol.MetricsUpdate{
		Timestamp: 200,
		Containers: []protocol.ContainerMetrics{
			{ID: "new-c", Name: "web", Project: "myapp", Service: "web", State: "running", CPUPercent: 99},
		},
	}
	model, _ := a.Update(MetricsMsg{m, testServer})
	a = model.(App)
	s = a.session()

	// Old buffer should be transferred to new container.
	if _, ok := s.CPUHistory["old-c"]; ok {
		t.Error("old-c buffer should be deleted after transfer")
	}
	newBuf := s.CPUHistory["new-c"]
	if newBuf == nil {
		t.Fatal("new-c buffer should exist")
	}
	// Should have 5 old points + 1 new point = 6.
	if newBuf.Len() != 6 {
		t.Errorf("new-c CPUHistory.Len() = %d, want 6", newBuf.Len())
	}
	data := newBuf.Data()
	// Last point should be the new streaming value.
	if data[5] != 99 {
		t.Errorf("last CPU value = %v, want 99", data[5])
	}
}

func TestStreamingBufferTransferNoServiceNoTransfer(t *testing.T) {
	a := newTestApp()
	s := a.session()

	// Non-compose container: no service label.
	s.ContInfo = []protocol.ContainerInfo{
		{ID: "old-c", Name: "web", State: "exited"},
	}
	s.CPUHistory["old-c"] = NewRingBuffer[float64](ringBufSize)
	s.CPUHistory["old-c"].Push(10)
	s.MemHistory["old-c"] = NewRingBuffer[float64](ringBufSize)

	// New container without service — should NOT transfer.
	m := &protocol.MetricsUpdate{
		Timestamp: 200,
		Containers: []protocol.ContainerMetrics{
			{ID: "new-c", Name: "web", State: "running", CPUPercent: 5},
		},
	}
	model, _ := a.Update(MetricsMsg{m, testServer})
	a = model.(App)
	s = a.session()

	// new-c should start fresh (1 point from streaming).
	if s.CPUHistory["new-c"].Len() != 1 {
		t.Errorf("new-c CPUHistory.Len() = %d, want 1 (no transfer)", s.CPUHistory["new-c"].Len())
	}
}
