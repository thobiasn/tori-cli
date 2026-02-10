package tui

import (
	"testing"

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

	// cursor=0 is group header "app".
	if id := cursorContainerID(groups, collapsed, 0); id != "" {
		t.Errorf("cursor=0 should be header, got %q", id)
	}
	// cursor=1 is c1.
	if id := cursorContainerID(groups, collapsed, 1); id != "c1" {
		t.Errorf("cursor=1 = %q, want c1", id)
	}
	// cursor=2 is c2.
	if id := cursorContainerID(groups, collapsed, 2); id != "c2" {
		t.Errorf("cursor=2 = %q, want c2", id)
	}
	// cursor=3 is header "other".
	if id := cursorContainerID(groups, collapsed, 3); id != "" {
		t.Errorf("cursor=3 should be header, got %q", id)
	}
	// cursor=4 is c3.
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

	// cursor=0 is "app" header, cursor=1 is "other" header, cursor=2 is c3.
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

func TestMaxCursorPos(t *testing.T) {
	groups := []containerGroup{
		{name: "app", containers: []protocol.ContainerMetrics{{ID: "c1"}, {ID: "c2"}}},
		{name: "other", containers: []protocol.ContainerMetrics{{ID: "c3"}}},
	}

	// Expanded: header + 2 + header + 1 = 5 items, max = 4.
	if max := maxCursorPos(groups, map[string]bool{}); max != 4 {
		t.Errorf("maxCursorPos expanded = %d, want 4", max)
	}

	// Collapsed "app": header + header + 1 = 3 items, max = 2.
	if max := maxCursorPos(groups, map[string]bool{"app": true}); max != 2 {
		t.Errorf("maxCursorPos collapsed = %d, want 2", max)
	}
}
