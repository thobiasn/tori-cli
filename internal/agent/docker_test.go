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
		prevCPU: make(map[string]cpuPrev),
		tracked: make(map[string]bool),
	}

	// Initially untracked (nothing in tracked set).
	if d.IsTracked("web") {
		t.Error("should be untracked by default")
	}

	// Track by name.
	d.SetTracking("web", "", true)
	if !d.IsTracked("web") {
		t.Error("web should be tracked after SetTracking(true)")
	}

	// Other containers still untracked.
	if d.IsTracked("api") {
		t.Error("api should still be untracked")
	}

	// Untrack.
	d.SetTracking("web", "", false)
	if d.IsTracked("web") {
		t.Error("web should be untracked again")
	}
}

func TestSetTrackingProject(t *testing.T) {
	d := &DockerCollector{
		prevCPU: make(map[string]cpuPrev),
		tracked: make(map[string]bool),
		// Simulate known containers in the project.
		lastContainers: []Container{
			{Name: "web", Project: "myapp"},
			{Name: "api", Project: "myapp"},
			{Name: "cache", Project: "other"},
		},
	}

	// Initially untracked.
	if d.IsTracked("web") {
		t.Error("web should be untracked by default")
	}

	// Track project — should track all containers in "myapp".
	d.SetTracking("", "myapp", true)
	if !d.IsTracked("web") {
		t.Error("web should be tracked")
	}
	if !d.IsTracked("api") {
		t.Error("api should be tracked")
	}
	if d.IsTracked("cache") {
		t.Error("cache in other project should still be untracked")
	}

	// Untrack project.
	d.SetTracking("", "myapp", false)
	if d.IsTracked("web") {
		t.Error("web should be untracked again")
	}
	if d.IsTracked("api") {
		t.Error("api should be untracked again")
	}
}

func TestSetTrackingProjectThenUntrackContainer(t *testing.T) {
	d := &DockerCollector{
		prevCPU: make(map[string]cpuPrev),
		tracked: make(map[string]bool),
		lastContainers: []Container{
			{Name: "web", Project: "myapp"},
			{Name: "api", Project: "myapp"},
		},
	}

	// Track project.
	d.SetTracking("", "myapp", true)
	if !d.IsTracked("web") || !d.IsTracked("api") {
		t.Fatal("both should be tracked after project toggle")
	}

	// Untrack individual container within the project.
	d.SetTracking("web", "", false)
	if d.IsTracked("web") {
		t.Error("web should be untracked after individual toggle")
	}
	if !d.IsTracked("api") {
		t.Error("api should still be tracked")
	}
}

func TestGetTrackingState(t *testing.T) {
	d := &DockerCollector{
		prevCPU: make(map[string]cpuPrev),
		tracked: make(map[string]bool),
	}

	d.SetTracking("web", "", true)
	d.SetTracking("api", "", true)
	d.SetTracking("db", "", false)

	state := d.GetTrackingState()
	if len(state) != 3 {
		t.Errorf("tracking state entries = %d, want 3", len(state))
	}
	if !state["web"] {
		t.Error("web should be tracked")
	}
	if !state["api"] {
		t.Error("api should be tracked")
	}
	if state["db"] {
		t.Error("db should be untracked")
	}
}

func TestLoadTrackingState(t *testing.T) {
	d := &DockerCollector{
		prevCPU: make(map[string]cpuPrev),
		tracked: make(map[string]bool),
	}

	d.LoadTrackingState(map[string]bool{"web": true, "api": true, "db": false})

	if !d.IsTracked("web") {
		t.Error("web should be tracked after load")
	}
	if !d.IsTracked("api") {
		t.Error("api should be tracked after load")
	}
	if d.IsTracked("db") {
		t.Error("db should be untracked after load")
	}
	state := d.GetTrackingState()
	if _, seen := state["db"]; !seen {
		t.Error("db should be present in tracking state as explicitly untracked")
	}
}

func TestSetTrackingPolicy(t *testing.T) {
	d := &DockerCollector{
		include: []string{"web-*"},
		exclude: nil,
		tracked: make(map[string]bool),
	}

	d.SetTrackingPolicy([]string{"api-*"}, []string{"api-test"})

	d.mu.RLock()
	if len(d.include) != 1 || d.include[0] != "api-*" {
		t.Errorf("include = %v, want [api-*]", d.include)
	}
	if len(d.exclude) != 1 || d.exclude[0] != "api-test" {
		t.Errorf("exclude = %v, want [api-test]", d.exclude)
	}
	d.mu.RUnlock()
}

func TestSetTrackingStoresFalse(t *testing.T) {
	d := &DockerCollector{
		prevCPU: make(map[string]cpuPrev),
		tracked: make(map[string]bool),
	}

	d.SetTracking("web", "", true)
	d.SetTracking("web", "", false)

	state := d.GetTrackingState()
	tracked, exists := state["web"]
	if !exists {
		t.Error("web should still be in tracking state after untrack")
	}
	if tracked {
		t.Error("web should be false (untracked)")
	}
}

