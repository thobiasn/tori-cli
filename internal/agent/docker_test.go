package agent

import "testing"

func TestInspectCache(t *testing.T) {
	d := &DockerCollector{
		prevCPU:      make(map[string]cpuPrev),
		inspectCache: make(map[string]inspectResult),
	}

	// Simulate caching for a non-running container.
	d.inspectCache["c1"] = inspectResult{
		health: "none", startedAt: 1000, restartCount: 2, exitCode: 137,
	}

	cached, ok := d.inspectCache["c1"]
	if !ok {
		t.Fatal("expected c1 in cache")
	}
	if cached.health != "none" || cached.exitCode != 137 {
		t.Errorf("cached values wrong: %+v", cached)
	}

	// Evict running container.
	delete(d.inspectCache, "c1")
	if _, ok := d.inspectCache["c1"]; ok {
		t.Error("c1 should be evicted")
	}
}

func TestInspectCacheStaleEviction(t *testing.T) {
	d := &DockerCollector{
		prevCPU:      make(map[string]cpuPrev),
		inspectCache: make(map[string]inspectResult),
	}

	// Populate cache with entries.
	d.inspectCache["c1"] = inspectResult{health: "none"}
	d.inspectCache["c2"] = inspectResult{health: "none"}
	d.inspectCache["c3"] = inspectResult{health: "none"}

	// Simulate end-of-Collect cleanup: only c1 and c3 are discovered.
	discovered := []Container{{ID: "c1"}, {ID: "c3"}}
	seen := make(map[string]bool, len(discovered))
	for _, c := range discovered {
		seen[c.ID] = true
	}
	for id := range d.inspectCache {
		if !seen[id] {
			delete(d.inspectCache, id)
		}
	}

	if _, ok := d.inspectCache["c2"]; ok {
		t.Error("c2 should be evicted (not in discovered)")
	}
	if _, ok := d.inspectCache["c1"]; !ok {
		t.Error("c1 should remain in cache")
	}
	if _, ok := d.inspectCache["c3"]; !ok {
		t.Error("c3 should remain in cache")
	}
}

func TestSetTrackingContainer(t *testing.T) {
	d := &DockerCollector{
		prevCPU:         make(map[string]cpuPrev),
		tracked:         make(map[string]bool),
		trackedProjects: make(map[string]bool),
	}

	// Initially untracked (nothing in tracked set).
	if d.IsTracked("web", "myapp") {
		t.Error("should be untracked by default")
	}

	// Track by name.
	d.SetTracking("web", "", true)
	if !d.IsTracked("web", "myapp") {
		t.Error("web should be tracked after SetTracking(true)")
	}

	// Other containers still untracked.
	if d.IsTracked("api", "myapp") {
		t.Error("api should still be untracked")
	}

	// Untrack.
	d.SetTracking("web", "", false)
	if d.IsTracked("web", "myapp") {
		t.Error("web should be untracked again")
	}
}

func TestSetTrackingProject(t *testing.T) {
	d := &DockerCollector{
		prevCPU:         make(map[string]cpuPrev),
		tracked:         make(map[string]bool),
		trackedProjects: make(map[string]bool),
	}

	// Initially untracked.
	if d.IsTracked("web", "myapp") {
		t.Error("web in myapp should be untracked by default")
	}

	// Track project.
	d.SetTracking("", "myapp", true)
	if !d.IsTracked("web", "myapp") {
		t.Error("web in myapp should be tracked")
	}
	if d.IsTracked("cache", "other") {
		t.Error("cache in other project should still be untracked")
	}

	// Untrack project.
	d.SetTracking("", "myapp", false)
	if d.IsTracked("web", "myapp") {
		t.Error("web in myapp should be untracked again")
	}
}

func TestGetTrackingState(t *testing.T) {
	d := &DockerCollector{
		prevCPU:         make(map[string]cpuPrev),
		tracked:         make(map[string]bool),
		trackedProjects: make(map[string]bool),
	}

	d.SetTracking("web", "", true)
	d.SetTracking("api", "", true)
	d.SetTracking("", "myapp", true)

	containers, projects := d.GetTrackingState()
	if len(containers) != 2 {
		t.Errorf("tracked containers = %d, want 2", len(containers))
	}
	if len(projects) != 1 {
		t.Errorf("tracked projects = %d, want 1", len(projects))
	}
}

func TestLoadTrackingState(t *testing.T) {
	d := &DockerCollector{
		prevCPU:         make(map[string]cpuPrev),
		tracked:         make(map[string]bool),
		trackedProjects: make(map[string]bool),
	}

	d.LoadTrackingState([]string{"web", "api"}, []string{"myapp"})

	if !d.IsTracked("web", "") {
		t.Error("web should be tracked after load")
	}
	if !d.IsTracked("api", "") {
		t.Error("api should be tracked after load")
	}
	if !d.IsTracked("anything", "myapp") {
		t.Error("anything in myapp should be tracked after load")
	}
	if d.IsTracked("db", "other") {
		t.Error("db in other should still be untracked")
	}
}

func TestSetFilters(t *testing.T) {
	d := &DockerCollector{
		include: []string{"web-*"},
		exclude: nil,
	}

	if !d.MatchFilter("web-app") {
		t.Error("web-app should match initial include")
	}
	if d.MatchFilter("api-server") {
		t.Error("api-server should not match initial include")
	}

	// Swap filters at runtime.
	d.SetFilters([]string{"api-*"}, []string{"api-test"})

	if d.MatchFilter("web-app") {
		t.Error("web-app should no longer match after SetFilters")
	}
	if !d.MatchFilter("api-server") {
		t.Error("api-server should match new include")
	}
	if d.MatchFilter("api-test") {
		t.Error("api-test should be excluded")
	}

	// Clear all filters.
	d.SetFilters(nil, nil)
	if !d.MatchFilter("anything") {
		t.Error("everything should match with no filters")
	}
}

func TestMatchFilterExported(t *testing.T) {
	d := &DockerCollector{include: []string{"web-*"}, exclude: []string{"web-test"}}
	if !d.MatchFilter("web-prod") {
		t.Error("web-prod should match")
	}
	if d.MatchFilter("web-test") {
		t.Error("web-test should be excluded")
	}
	if d.MatchFilter("api") {
		t.Error("api should not match include")
	}
}

