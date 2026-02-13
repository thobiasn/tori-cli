package tui

import (
	"net"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/thobiasn/rook/internal/protocol"
)

func TestBuildGroupsSorting(t *testing.T) {
	containers := []protocol.ContainerMetrics{
		{ID: "c1", Name: "web", State: "running"},
		{ID: "c2", Name: "db", State: "running"},
		{ID: "c3", Name: "cron", State: "exited"},
		{ID: "c4", Name: "standalone", State: "running"},
	}
	contInfo := []protocol.ContainerInfo{
		{ID: "c1", Name: "web", Project: "myapp"},
		{ID: "c2", Name: "db", Project: "myapp"},
		{ID: "c3", Name: "cron", Project: "backups"},
		{ID: "c4", Name: "standalone"},
	}

	groups := buildGroups(containers, contInfo)

	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}

	// Alpha sorted, "other" last.
	if groups[0].name != "backups" {
		t.Errorf("groups[0].name = %q, want backups", groups[0].name)
	}
	if groups[1].name != "myapp" {
		t.Errorf("groups[1].name = %q, want myapp", groups[1].name)
	}
	if groups[2].name != "other" {
		t.Errorf("groups[2].name = %q, want other", groups[2].name)
	}
}

func TestBuildGroupsRunningCount(t *testing.T) {
	containers := []protocol.ContainerMetrics{
		{ID: "c1", State: "running"},
		{ID: "c2", State: "exited"},
		{ID: "c3", State: "running"},
	}
	contInfo := []protocol.ContainerInfo{
		{ID: "c1", Project: "app"},
		{ID: "c2", Project: "app"},
		{ID: "c3", Project: "app"},
	}

	groups := buildGroups(containers, contInfo)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].running != 2 {
		t.Errorf("running = %d, want 2", groups[0].running)
	}
}

func TestBuildGroupsEmpty(t *testing.T) {
	groups := buildGroups(nil, nil)
	if len(groups) != 0 {
		t.Errorf("expected 0 groups, got %d", len(groups))
	}
}

func TestBuildGroupsUntrackedInjected(t *testing.T) {
	// Metrics only contain tracked containers.
	containers := []protocol.ContainerMetrics{
		{ID: "c1", Name: "web", State: "running", CPUPercent: 5.0},
	}
	// ContInfo has both tracked and untracked.
	contInfo := []protocol.ContainerInfo{
		{ID: "c1", Name: "web", Project: "app", Tracked: true},
		{ID: "c2", Name: "db", Project: "app", Tracked: false, State: "running"},
	}

	groups := buildGroups(containers, contInfo)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	g := groups[0]
	if g.name != "app" {
		t.Errorf("group name = %q, want app", g.name)
	}
	if len(g.containers) != 2 {
		t.Fatalf("expected 2 containers in group, got %d", len(g.containers))
	}

	// Find the untracked container (db) — it should be injected as a stub.
	var found bool
	for _, c := range g.containers {
		if c.ID == "c2" {
			found = true
			if c.Name != "db" {
				t.Errorf("stub name = %q, want db", c.Name)
			}
			if c.CPUPercent != 0 {
				t.Errorf("stub CPU = %f, want 0 (zero stats)", c.CPUPercent)
			}
		}
	}
	if !found {
		t.Error("untracked container c2 was not injected into groups")
	}
}

func TestCursorContainerID(t *testing.T) {
	groups := []containerGroup{
		{name: "app", containers: []protocol.ContainerMetrics{
			{ID: "c1"}, {ID: "c2"},
		}},
		{name: "other", containers: []protocol.ContainerMetrics{
			{ID: "c3"},
		}},
	}
	collapsed := map[string]bool{}

	if id := cursorContainerID(groups, collapsed, 0); id != "" {
		t.Errorf("cursor=0 should be header, got %q", id)
	}
	if id := cursorContainerID(groups, collapsed, 1); id != "c1" {
		t.Errorf("cursor=1 = %q, want c1", id)
	}
	if id := cursorContainerID(groups, collapsed, 2); id != "c2" {
		t.Errorf("cursor=2 = %q, want c2", id)
	}
	if id := cursorContainerID(groups, collapsed, 3); id != "" {
		t.Errorf("cursor=3 should be header, got %q", id)
	}
	if id := cursorContainerID(groups, collapsed, 4); id != "c3" {
		t.Errorf("cursor=4 = %q, want c3", id)
	}
}

func TestCursorContainerIDCollapsed(t *testing.T) {
	groups := []containerGroup{
		{name: "app", containers: []protocol.ContainerMetrics{
			{ID: "c1"}, {ID: "c2"},
		}},
		{name: "other", containers: []protocol.ContainerMetrics{
			{ID: "c3"},
		}},
	}
	collapsed := map[string]bool{"app": true}

	if id := cursorContainerID(groups, collapsed, 0); id != "" {
		t.Errorf("cursor=0 should be header, got %q", id)
	}
	if id := cursorContainerID(groups, collapsed, 1); id != "" {
		t.Errorf("cursor=1 should be header, got %q", id)
	}
	if id := cursorContainerID(groups, collapsed, 2); id != "c3" {
		t.Errorf("cursor=2 = %q, want c3", id)
	}
}

func TestCursorGroupName(t *testing.T) {
	groups := []containerGroup{
		{name: "app", containers: []protocol.ContainerMetrics{{ID: "c1"}, {ID: "c2"}}},
		{name: "other", containers: []protocol.ContainerMetrics{{ID: "c3"}}},
	}
	collapsed := map[string]bool{}

	if name := cursorGroupName(groups, collapsed, 0); name != "app" {
		t.Errorf("cursor=0 group name = %q, want app", name)
	}
	if name := cursorGroupName(groups, collapsed, 1); name != "" {
		t.Errorf("cursor=1 should not be header, got %q", name)
	}
	if name := cursorGroupName(groups, collapsed, 3); name != "other" {
		t.Errorf("cursor=3 group name = %q, want other", name)
	}

	collapsed["app"] = true
	if name := cursorGroupName(groups, collapsed, 0); name != "app" {
		t.Errorf("collapsed cursor=0 = %q, want app", name)
	}
	if name := cursorGroupName(groups, collapsed, 1); name != "other" {
		t.Errorf("collapsed cursor=1 = %q, want other", name)
	}
}

func TestUpdateDashboardCursorNav(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Dash.groups = []containerGroup{
		{name: "app", containers: []protocol.ContainerMetrics{{ID: "c1"}, {ID: "c2"}}},
	}

	updateDashboard(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if s.Dash.cursor != 1 {
		t.Errorf("cursor after j = %d, want 1", s.Dash.cursor)
	}

	updateDashboard(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if s.Dash.cursor != 2 {
		t.Errorf("cursor after j = %d, want 2", s.Dash.cursor)
	}

	// At end (max=2), shouldn't go further.
	updateDashboard(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if s.Dash.cursor != 2 {
		t.Errorf("cursor should stay at 2, got %d", s.Dash.cursor)
	}

	updateDashboard(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if s.Dash.cursor != 1 {
		t.Errorf("cursor after k = %d, want 1", s.Dash.cursor)
	}
}

func TestUpdateDashboardCollapseToggle(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Dash.groups = []containerGroup{
		{name: "app", containers: []protocol.ContainerMetrics{{ID: "c1"}}},
	}
	s.Dash.cursor = 0

	updateDashboard(&a, s, tea.KeyMsg{Type: tea.KeySpace})
	if !s.Dash.collapsed["app"] {
		t.Error("app should be collapsed after space")
	}

	updateDashboard(&a, s, tea.KeyMsg{Type: tea.KeySpace})
	if s.Dash.collapsed["app"] {
		t.Error("app should be expanded after second space")
	}
}

func TestUpdateDashboardToggleTrackingContainer(t *testing.T) {
	a := newTestApp()
	s := a.session()
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	s.Client = NewClient(c1, testServer)
	s.Dash.groups = []containerGroup{
		{name: "app", containers: []protocol.ContainerMetrics{
			{ID: "c1", Name: "web"}, {ID: "c2", Name: "db"},
		}},
	}
	s.ContInfo = []protocol.ContainerInfo{
		{ID: "c1", Name: "web", Project: "app", Tracked: true},
		{ID: "c2", Name: "db", Project: "app", Tracked: true},
	}
	s.Dash.cursor = 1

	cmd := updateDashboard(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if cmd == nil {
		t.Error("expected non-nil cmd for tracking toggle on container")
	}
}

func TestUpdateDashboardToggleTrackingGroup(t *testing.T) {
	a := newTestApp()
	s := a.session()
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	s.Client = NewClient(c1, testServer)
	s.Dash.groups = []containerGroup{
		{name: "app", containers: []protocol.ContainerMetrics{
			{ID: "c1", Name: "web"},
		}},
	}
	s.ContInfo = []protocol.ContainerInfo{
		{ID: "c1", Name: "web", Project: "app", Tracked: true},
	}
	s.Dash.cursor = 0

	cmd := updateDashboard(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if cmd == nil {
		t.Error("expected non-nil cmd for tracking toggle on group")
	}
}

func TestUpdateDashboardToggleTrackingOtherGroup(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Dash.groups = []containerGroup{
		{name: "other", containers: []protocol.ContainerMetrics{
			{ID: "c1", Name: "standalone"},
		}},
	}
	s.Dash.cursor = 0

	cmd := updateDashboard(&a, s, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if cmd != nil {
		t.Error("'t' on 'other' group header should return nil cmd")
	}
}

func TestIsProjectTracked(t *testing.T) {
	info := []protocol.ContainerInfo{
		{ID: "c1", Project: "app", Tracked: true},
		{ID: "c2", Project: "app", Tracked: false},
		{ID: "c3", Project: "db", Tracked: false},
	}

	if !isProjectTracked("app", info) {
		t.Error("app should be tracked (c1 is tracked)")
	}
	if isProjectTracked("db", info) {
		t.Error("db should not be tracked")
	}
	if isProjectTracked("nonexistent", info) {
		t.Error("nonexistent should not be tracked")
	}
}

func TestIsContainerTracked(t *testing.T) {
	info := []protocol.ContainerInfo{
		{ID: "c1", Tracked: true},
		{ID: "c2", Tracked: false},
	}

	if !isContainerTracked("c1", info) {
		t.Error("c1 should be tracked")
	}
	if isContainerTracked("c2", info) {
		t.Error("c2 should not be tracked")
	}
	if isContainerTracked("c3", info) {
		t.Error("unknown container should default to untracked")
	}
}

func TestUpdateDashboardEnterGroupHeader(t *testing.T) {
	a := newTestApp()
	s := a.session()
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	s.Client = NewClient(c1, testServer)
	s.Dash.groups = []containerGroup{
		{name: "myapp", containers: []protocol.ContainerMetrics{
			{ID: "c1", Name: "web"}, {ID: "c2", Name: "db"},
		}},
	}
	s.ContInfo = []protocol.ContainerInfo{
		{ID: "c1", Name: "web", Project: "myapp"},
		{ID: "c2", Name: "db", Project: "myapp"},
	}
	s.Dash.cursor = 0 // on group header

	updateDashboard(&a, s, tea.KeyMsg{Type: tea.KeyEnter})
	if a.active != viewDetail {
		t.Errorf("enter on group header should open detail, got %d", a.active)
	}
	if s.Detail.project != "myapp" {
		t.Errorf("detail.project = %q, want myapp", s.Detail.project)
	}
	if s.Detail.containerID != "" {
		t.Errorf("detail.containerID should be empty in group mode, got %q", s.Detail.containerID)
	}
	if len(s.Detail.projectIDs) != 2 {
		t.Errorf("detail.projectIDs len = %d, want 2", len(s.Detail.projectIDs))
	}
}

func TestUpdateDashboardEnterOtherGroupNoOp(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Dash.groups = []containerGroup{
		{name: "other", containers: []protocol.ContainerMetrics{
			{ID: "c1", Name: "standalone"},
		}},
	}
	s.Dash.cursor = 0

	updateDashboard(&a, s, tea.KeyMsg{Type: tea.KeyEnter})
	if a.active != viewDashboard {
		t.Errorf("enter on 'other' group should stay on dashboard, got %d", a.active)
	}
}

func TestCursorContainerMetrics(t *testing.T) {
	groups := []containerGroup{
		{name: "app", containers: []protocol.ContainerMetrics{
			{ID: "c1", Name: "web"}, {ID: "c2", Name: "db"},
		}},
		{name: "other", containers: []protocol.ContainerMetrics{
			{ID: "c3", Name: "cache"},
		}},
	}
	collapsed := map[string]bool{}

	g, idx := cursorContainerMetrics(groups, collapsed, 0)
	if g == nil || g.name != "app" || idx != -1 {
		t.Errorf("cursor=0: got group=%v idx=%d, want app/-1", g, idx)
	}

	g, idx = cursorContainerMetrics(groups, collapsed, 1)
	if g == nil || idx != 0 || g.containers[idx].ID != "c1" {
		t.Errorf("cursor=1: got idx=%d, want c1", idx)
	}

	g, idx = cursorContainerMetrics(groups, collapsed, 2)
	if g == nil || idx != 1 || g.containers[idx].ID != "c2" {
		t.Errorf("cursor=2: got idx=%d, want c2", idx)
	}

	g, idx = cursorContainerMetrics(groups, collapsed, 3)
	if g == nil || g.name != "other" || idx != -1 {
		t.Errorf("cursor=3: got group=%v idx=%d, want other/-1", g, idx)
	}

	g, idx = cursorContainerMetrics(groups, collapsed, 4)
	if g == nil || idx != 0 || g.containers[idx].ID != "c3" {
		t.Errorf("cursor=4: got idx=%d, want c3", idx)
	}

	g, idx = cursorContainerMetrics(groups, collapsed, 99)
	if g != nil {
		t.Errorf("cursor=99: expected nil group, got %v", g)
	}

	collapsed["app"] = true
	g, idx = cursorContainerMetrics(groups, collapsed, 1)
	if g == nil || g.name != "other" || idx != -1 {
		t.Errorf("collapsed cursor=1: got group=%v idx=%d, want other/-1", g, idx)
	}
}

func TestMaxCursorPos(t *testing.T) {
	groups := []containerGroup{
		{name: "app", containers: []protocol.ContainerMetrics{{ID: "c1"}, {ID: "c2"}}},
		{name: "other", containers: []protocol.ContainerMetrics{{ID: "c3"}}},
	}

	if max := maxCursorPos(groups, map[string]bool{}); max != 4 {
		t.Errorf("maxCursorPos expanded = %d, want 4", max)
	}

	if max := maxCursorPos(groups, map[string]bool{"app": true}); max != 2 {
		t.Errorf("maxCursorPos collapsed = %d, want 2", max)
	}
}
