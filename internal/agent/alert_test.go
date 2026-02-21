package agent

import (
	"context"
	"sync"
	"testing"
	"time"
)

// testAlerter creates an Alerter with a test store and injectable clock.
func testAlerter(t *testing.T, alerts map[string]AlertConfig) (*Alerter, *Store) {
	t.Helper()
	s := testStore(t)
	n := NewNotifier(&NotifyConfig{})
	a, err := NewAlerter(alerts, s, n)
	if err != nil {
		t.Fatal(err)
	}
	return a, s
}

func TestAlertStateTransitions(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// CPU at 50% — should stay inactive.
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 50}})
	// Inactive instances get GC'd from the map, so check firing didn't happen.
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("alert rows = %d, want 0 (should not fire)", count)
	}

	// CPU at 95% — for=0 so should fire immediately.
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	inst := a.instances["high_cpu"]
	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected firing state")
	}

	// Verify alert row in DB.
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE rule_name = 'high_cpu'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1", count)
	}

	// CPU back to 50% — should resolve.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 50}})
	// Instance was seen this cycle (host data present), so it stays in map as inactive.
	// It will be GC'd on a subsequent cycle if unseen.
	inst = a.instances["high_cpu"]
	if inst != nil && inst.state != stateInactive {
		t.Fatalf("expected inactive after resolve, got %d", inst.state)
	}

	// Verify resolved_at is set.
	var resolvedAt *int64
	if err := s.db.QueryRow("SELECT resolved_at FROM alerts WHERE rule_name = 'high_cpu'").Scan(&resolvedAt); err != nil {
		t.Fatal(err)
	}
	if resolvedAt == nil {
		t.Error("resolved_at should be set")
	}
}

func TestAlertReFireAfterResolve(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Fire.
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	// Resolve.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 50}})
	// Fire again.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})

	inst := a.instances["high_cpu"]
	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected re-fire after resolve")
	}

	// Should have 2 alert rows.
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE rule_name = 'high_cpu'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("alert rows = %d, want 2 (fire, resolve, re-fire)", count)
	}
}

func TestAlertFiringStaysFiring(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()
	a.now = func() time.Time { return time.Now() }

	snap := &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}}

	// Fire.
	a.Evaluate(ctx, snap)
	// Evaluate again with condition still true — should stay firing, no new DB row.
	a.Evaluate(ctx, snap)
	a.Evaluate(ctx, snap)

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1 (should not create duplicates)", count)
	}
}

func TestAlertForDuration(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			For:       Duration{30 * time.Second},
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	snap := &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}}

	// First eval — should go to pending, not firing.
	a.Evaluate(ctx, snap)
	inst := a.instances["high_cpu"]
	if inst == nil || inst.state != statePending {
		t.Fatalf("expected pending, got %v", inst)
	}

	// 15s later — still pending.
	now = now.Add(15 * time.Second)
	a.Evaluate(ctx, snap)
	if inst.state != statePending {
		t.Fatalf("expected still pending at 15s, got %d", inst.state)
	}

	// 30s later — should fire.
	now = now.Add(15 * time.Second)
	a.Evaluate(ctx, snap)
	if inst.state != stateFiring {
		t.Fatalf("expected firing at 30s, got %d", inst.state)
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE rule_name = 'high_cpu'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1", count)
	}
}

func TestAlertForDurationResetOnFalse(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			For:       Duration{30 * time.Second},
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Go pending.
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	inst := a.instances["high_cpu"]
	if inst == nil || inst.state != statePending {
		t.Fatalf("expected pending")
	}

	// Condition false before for elapsed — back to inactive (and GC'd).
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 50}})
	// Inactive instances with seen=true stay in map until next unseen cycle,
	// but the state should be inactive.
	if inst.state != stateInactive {
		t.Fatalf("expected inactive, got %d", inst.state)
	}
}

func TestContainerAlertPerInstance(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()
	a.now = func() time.Time { return time.Now() }

	snap := &MetricSnapshot{
		Containers: []ContainerMetrics{
			{ID: "aaa", Name: "web", State: "exited"},
			{ID: "bbb", Name: "api", State: "running"},
		},
	}

	a.Evaluate(ctx, snap)

	// Only "web" should fire.
	if inst, ok := a.instances["exited:aaa"]; !ok || inst.state != stateFiring {
		t.Error("expected exited:aaa to be firing")
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1", count)
	}
}

func TestContainerAlertNumeric(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "container.cpu_percent > 80",
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()
	a.now = func() time.Time { return time.Now() }

	snap := &MetricSnapshot{
		Containers: []ContainerMetrics{
			{ID: "aaa", Name: "web", State: "running", CPUPercent: 95},
			{ID: "bbb", Name: "api", State: "running", CPUPercent: 20},
		},
	}

	a.Evaluate(ctx, snap)

	if inst, ok := a.instances["high_cpu:aaa"]; !ok || inst.state != stateFiring {
		t.Error("expected high_cpu:aaa to be firing")
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1 (only one container over threshold)", count)
	}
}

func TestDiskAlertPerMountpoint(t *testing.T) {
	alerts := map[string]AlertConfig{
		"disk_full": {
			Condition: "host.disk_percent > 90",
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()
	a.now = func() time.Time { return time.Now() }

	snap := &MetricSnapshot{
		Host: &HostMetrics{},
		Disks: []DiskMetrics{
			{Mountpoint: "/", Percent: 95},
			{Mountpoint: "/home", Percent: 40},
		},
	}

	a.Evaluate(ctx, snap)

	if inst, ok := a.instances["disk_full:/"]; !ok || inst.state != stateFiring {
		t.Error("expected disk_full:/ to be firing")
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1", count)
	}
}

func TestStaleInstanceCleanup(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Container appears and fires.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})
	if a.instances["exited:aaa"].state != stateFiring {
		t.Fatal("expected firing")
	}

	// Container disappears — stale firing instance should be resolved and GC'd.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Containers: []ContainerMetrics{}})

	if _, exists := a.instances["exited:aaa"]; exists {
		t.Error("expected stale instance to be GC'd")
	}

	var resolvedAt *int64
	if err := s.db.QueryRow("SELECT resolved_at FROM alerts WHERE instance_key = 'exited:aaa'").Scan(&resolvedAt); err != nil {
		t.Fatal(err)
	}
	if resolvedAt == nil {
		t.Error("resolved_at should be set for stale instance")
	}
}

func TestStalePendingInstanceCleanup(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			For:       Duration{30 * time.Second},
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Container appears and goes pending.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})
	if a.instances["exited:aaa"].state != statePending {
		t.Fatal("expected pending")
	}

	// Container disappears — stale pending instance should be reset and GC'd.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Containers: []ContainerMetrics{}})

	if _, exists := a.instances["exited:aaa"]; exists {
		t.Error("expected stale pending instance to be GC'd")
	}
}

func TestNilSnapshotDoesNotFalseResolve(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)
	ctx := context.Background()
	a.now = func() time.Time { return time.Now() }

	// Fire both alerts.
	a.Evaluate(ctx, &MetricSnapshot{
		Host:       &HostMetrics{CPUPercent: 95},
		Containers: []ContainerMetrics{{ID: "aaa", State: "exited"}},
	})
	if a.instances["high_cpu"].state != stateFiring {
		t.Fatal("expected high_cpu firing")
	}
	if a.instances["exited:aaa"].state != stateFiring {
		t.Fatal("expected exited:aaa firing")
	}

	// Collection fails — nil snapshot fields should NOT resolve active alerts.
	a.Evaluate(ctx, &MetricSnapshot{})

	if a.instances["high_cpu"].state != stateFiring {
		t.Error("nil host should not resolve active host alert")
	}
	if a.instances["exited:aaa"].state != stateFiring {
		t.Error("nil containers should not resolve active container alert")
	}
}

func TestInstancesGarbageCollected(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)
	ctx := context.Background()
	a.now = func() time.Time { return time.Now() }

	// Container appears but doesn't match — instance is inactive.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "running"}},
	})

	// Next cycle, container disappears — inactive unseen instance should be GC'd.
	a.Evaluate(ctx, &MetricSnapshot{Containers: []ContainerMetrics{}})

	if len(a.instances) != 0 {
		t.Errorf("instances map should be empty, has %d entries", len(a.instances))
	}
}

func TestDeterministicRuleOrder(t *testing.T) {
	// Create rules in a map — order is non-deterministic.
	alerts := map[string]AlertConfig{
		"zzz": {Condition: "host.cpu_percent > 90", Severity: "warning", Actions: []string{"notify"}},
		"aaa": {Condition: "host.cpu_percent > 80", Severity: "warning", Actions: []string{"notify"}},
		"mmm": {Condition: "host.cpu_percent > 85", Severity: "warning", Actions: []string{"notify"}},
	}

	a, _ := testAlerter(t, alerts)
	if len(a.rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(a.rules))
	}
	if a.rules[0].name != "aaa" || a.rules[1].name != "mmm" || a.rules[2].name != "zzz" {
		t.Errorf("rules not sorted: %s, %s, %s", a.rules[0].name, a.rules[1].name, a.rules[2].name)
	}
}

func TestAlertSilence(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Silence for 1 minute.
	a.Silence("high_cpu", 1*time.Minute)

	if !a.isSilenced("high_cpu") {
		t.Fatal("expected silenced")
	}

	// Advance past silence duration.
	now = now.Add(2 * time.Minute)
	if a.isSilenced("high_cpu") {
		t.Fatal("expected silence expired")
	}

	// Non-silenced rule.
	if a.isSilenced("nonexistent") {
		t.Fatal("nonexistent rule should not be silenced")
	}
}

func TestAlertStateChangeCallback(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	var callbacks []string
	a.onStateChange = func(alert *Alert, state string) {
		callbacks = append(callbacks, state)
	}

	// Fire.
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	// Resolve.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 50}})

	if len(callbacks) != 2 {
		t.Fatalf("expected 2 callbacks, got %d", len(callbacks))
	}
	if callbacks[0] != "firing" || callbacks[1] != "resolved" {
		t.Errorf("callbacks = %v, want [firing, resolved]", callbacks)
	}
}

func TestAlertSilenceSuppressesNotify(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Silence the rule.
	a.Silence("exited", 5*time.Minute)

	// Fire — notify should be suppressed, but alert should still be created.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})

	inst := a.instances["exited:aaa"]
	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected exited:aaa to be firing even when silenced")
	}

	// Alert should still be recorded in DB.
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE rule_name = 'exited'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1 (alert should be recorded even when silenced)", count)
	}
}

func TestAlertResolveCallbackFields(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	var fired, resolved *Alert
	a.onStateChange = func(alert *Alert, state string) {
		if state == "firing" {
			fired = alert
		} else if state == "resolved" {
			resolved = alert
		}
	}

	// Fire.
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	// Resolve.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 50}})

	if fired == nil || resolved == nil {
		t.Fatal("expected both firing and resolved callbacks")
	}

	// Verify resolved callback has full fields.
	if resolved.RuleName != "high_cpu" {
		t.Errorf("resolved.RuleName = %q, want high_cpu", resolved.RuleName)
	}
	if resolved.Severity != "critical" {
		t.Errorf("resolved.Severity = %q, want critical", resolved.Severity)
	}
	if resolved.Condition != "host.cpu_percent > 90" {
		t.Errorf("resolved.Condition = %q, want 'host.cpu_percent > 90'", resolved.Condition)
	}
	if resolved.ID != fired.ID {
		t.Errorf("resolved.ID = %d, want %d (same as fired)", resolved.ID, fired.ID)
	}
	if resolved.ResolvedAt == nil {
		t.Error("resolved.ResolvedAt should be set")
	}
}

func TestEvaluateContainerEventStateFires(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Container dies — should fire immediately via event-driven path.
	a.EvaluateContainerEvent(ctx, ContainerMetrics{
		ID:    "aaa",
		Name:  "web",
		State: "exited",
	})

	a.mu.Lock()
	inst := a.instances["exited:aaa"]
	a.mu.Unlock()

	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected exited:aaa to be firing")
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE rule_name = 'exited'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1", count)
	}
}

func TestEvaluateContainerEventSkipsNumericRules(t *testing.T) {
	// EvaluateContainerEvent now skips numeric rules entirely — events don't
	// carry metric data (CPU/mem = 0), which would cause false resolution.
	// Only string fields (state, health) are evaluated via events.
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "container.cpu_percent > 80",
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Fire high_cpu via regular Evaluate with real CPU data.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "running", CPUPercent: 95}},
	})
	if a.instances["high_cpu:aaa"].state != stateFiring {
		t.Fatal("expected high_cpu:aaa firing")
	}

	// Event evaluation should skip the numeric rule entirely — alert stays firing.
	now = now.Add(10 * time.Second)
	a.EvaluateContainerEvent(ctx, ContainerMetrics{
		ID:    "aaa",
		Name:  "web",
		State: "running",
	})

	if a.instances["high_cpu:aaa"].state != stateFiring {
		t.Error("expected high_cpu:aaa to still be firing (numeric rules skipped by event eval)")
	}
}

func TestEvaluateContainerEventSkipsHostRules(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()
	a.now = func() time.Time { return time.Now() }

	// Only the container rule should be evaluated.
	a.EvaluateContainerEvent(ctx, ContainerMetrics{
		ID:    "aaa",
		Name:  "web",
		State: "exited",
	})

	a.mu.Lock()
	_, hasHostInst := a.instances["high_cpu"]
	inst := a.instances["exited:aaa"]
	a.mu.Unlock()

	if hasHostInst {
		t.Error("host rule should not be evaluated by EvaluateContainerEvent")
	}
	if inst == nil || inst.state != stateFiring {
		t.Error("container rule should fire")
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1 (only container rule)", count)
	}
}

func TestEvaluateContainerEventForDurationPending(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			For:       Duration{30 * time.Second},
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// First event — should go to pending (for > 0).
	a.EvaluateContainerEvent(ctx, ContainerMetrics{
		ID: "aaa", Name: "web", State: "exited",
	})

	a.mu.Lock()
	inst := a.instances["exited:aaa"]
	a.mu.Unlock()

	if inst == nil || inst.state != statePending {
		t.Fatal("expected pending state with for-duration")
	}

	// Second event before for elapsed — still pending.
	now = now.Add(15 * time.Second)
	a.EvaluateContainerEvent(ctx, ContainerMetrics{
		ID: "aaa", Name: "web", State: "exited",
	})

	a.mu.Lock()
	if a.instances["exited:aaa"].state != statePending {
		t.Fatal("expected still pending at 15s")
	}
	a.mu.Unlock()

	// Third event after for elapsed — should fire.
	now = now.Add(15 * time.Second)
	a.EvaluateContainerEvent(ctx, ContainerMetrics{
		ID: "aaa", Name: "web", State: "exited",
	})

	a.mu.Lock()
	if a.instances["exited:aaa"].state != stateFiring {
		t.Fatal("expected firing after for-duration elapsed")
	}
	a.mu.Unlock()
}

func TestEvaluateContainerEventResolution(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Fire.
	a.EvaluateContainerEvent(ctx, ContainerMetrics{
		ID: "aaa", Name: "web", State: "exited",
	})

	a.mu.Lock()
	if a.instances["exited:aaa"].state != stateFiring {
		t.Fatal("expected firing")
	}
	a.mu.Unlock()

	// Container starts — condition no longer matches, should resolve.
	now = now.Add(10 * time.Second)
	a.EvaluateContainerEvent(ctx, ContainerMetrics{
		ID: "aaa", Name: "web", State: "running",
	})

	a.mu.Lock()
	inst := a.instances["exited:aaa"]
	a.mu.Unlock()

	if inst != nil && inst.state != stateInactive {
		t.Errorf("expected inactive after resolution, got %d", inst.state)
	}

	// Verify resolved_at is set in DB.
	var resolvedAt *int64
	if err := s.db.QueryRow("SELECT resolved_at FROM alerts WHERE rule_name = 'exited'").Scan(&resolvedAt); err != nil {
		t.Fatal(err)
	}
	if resolvedAt == nil {
		t.Error("resolved_at should be set")
	}
}

func TestEvaluateContainerEventNoStaleCleanup(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Fire for container aaa via regular Evaluate.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})
	if a.instances["exited:aaa"].state != stateFiring {
		t.Fatal("expected firing")
	}

	// EvaluateContainerEvent for a different container should NOT clean up aaa.
	now = now.Add(10 * time.Second)
	a.EvaluateContainerEvent(ctx, ContainerMetrics{
		ID: "bbb", Name: "api", State: "running",
	})

	a.mu.Lock()
	inst := a.instances["exited:aaa"]
	a.mu.Unlock()

	if inst == nil || inst.state != stateFiring {
		t.Error("exited:aaa should still be firing — EvaluateContainerEvent must not do stale cleanup")
	}
}

func TestEvaluateContainerEventConcurrent(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)
	a.now = func() time.Time { return time.Now() }
	ctx := context.Background()

	// Run Evaluate and EvaluateContainerEvent concurrently to detect races.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			a.Evaluate(ctx, &MetricSnapshot{
				Host:       &HostMetrics{CPUPercent: 50},
				Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "running"}},
			})
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			a.EvaluateContainerEvent(ctx, ContainerMetrics{
				ID: "aaa", Name: "web", State: "exited",
			})
		}
	}()

	wg.Wait()
	// No assertions beyond "no race/panic" — run with -race to verify.
}

func TestNilDiskSnapshotDoesNotFalseResolve(t *testing.T) {
	alerts := map[string]AlertConfig{
		"disk_full": {
			Condition: "host.disk_percent > 90",
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)
	ctx := context.Background()
	a.now = func() time.Time { return time.Now() }

	// Fire.
	a.Evaluate(ctx, &MetricSnapshot{
		Disks: []DiskMetrics{{Mountpoint: "/", Percent: 95}},
	})
	if a.instances["disk_full:/"].state != stateFiring {
		t.Fatal("expected firing")
	}

	// Nil disks (collection failed) should NOT resolve.
	a.Evaluate(ctx, &MetricSnapshot{Disks: nil})
	if a.instances["disk_full:/"].state != stateFiring {
		t.Error("nil disks should not resolve active disk alert")
	}
}

func TestHostLoadAlert(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_load": {
			Condition: "host.load1 > 4",
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()
	a.now = func() time.Time { return time.Now() }

	// Load below threshold — no alert.
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{Load1: 2.0}})
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("alert rows = %d, want 0", count)
	}

	// Load above threshold — fires.
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{Load1: 5.0}})
	inst := a.instances["high_load"]
	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected firing")
	}
}

func TestHostSwapAlert(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_swap": {
			Condition: "host.swap_percent > 80",
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()
	a.now = func() time.Time { return time.Now() }

	// SwapTotal=0 — swap_percent should be 0, no alert.
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{SwapTotal: 0, SwapUsed: 0}})
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("alert rows = %d, want 0 (SwapTotal=0)", count)
	}

	// 90% swap used — fires.
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{SwapTotal: 1000, SwapUsed: 900}})
	inst := a.instances["high_swap"]
	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected firing with 90% swap")
	}

	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1", count)
	}
}

func TestContainerHealthAlert(t *testing.T) {
	alerts := map[string]AlertConfig{
		"unhealthy": {
			Condition: "container.health == 'unhealthy'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Healthy container — no fire.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", Health: "healthy"}},
	})
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("alert rows = %d, want 0", count)
	}

	// Unhealthy — fires.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", Health: "unhealthy"}},
	})
	inst := a.instances["unhealthy:aaa"]
	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected firing")
	}

	// Back to healthy — resolves.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", Health: "healthy"}},
	})
	inst = a.instances["unhealthy:aaa"]
	if inst != nil && inst.state != stateInactive {
		t.Errorf("expected resolved, got state %d", inst.state)
	}
}

func TestContainerRestartCountAlert(t *testing.T) {
	alerts := map[string]AlertConfig{
		"restarts": {
			Condition: "container.restart_count > 3",
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()
	a.now = func() time.Time { return time.Now() }

	// Low restart count — no fire.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", RestartCount: 1}},
	})
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("alert rows = %d, want 0", count)
	}

	// High restart count — fires.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", RestartCount: 5}},
	})
	inst := a.instances["restarts:aaa"]
	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected firing")
	}
}

func TestDiskMountpointDisappears(t *testing.T) {
	alerts := map[string]AlertConfig{
		"disk_full": {
			Condition: "host.disk_percent > 90",
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Fire for "/" mountpoint.
	a.Evaluate(ctx, &MetricSnapshot{
		Disks: []DiskMetrics{{Mountpoint: "/", Percent: 95}},
	})
	if inst, ok := a.instances["disk_full:/"]; !ok || inst.state != stateFiring {
		t.Fatal("expected disk_full:/ firing")
	}

	// Mountpoint "/" disappears (e.g. unmounted), but Disks is not nil.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{
		Disks: []DiskMetrics{{Mountpoint: "/home", Percent: 40}},
	})

	// Stale instance should be resolved and GC'd.
	if _, exists := a.instances["disk_full:/"]; exists {
		t.Error("expected disk_full:/ to be GC'd after mountpoint disappeared")
	}

	var resolvedAt *int64
	if err := s.db.QueryRow("SELECT resolved_at FROM alerts WHERE instance_key = 'disk_full:/'").Scan(&resolvedAt); err != nil {
		t.Fatal(err)
	}
	if resolvedAt == nil {
		t.Error("resolved_at should be set for disappeared mountpoint")
	}
}

func TestQueryRules(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			Severity:  "critical",
			For:       Duration{30 * time.Second},
			Actions:   []string{"notify"},
		},
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// No firing instances yet.
	rules := a.QueryRules()
	if len(rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(rules))
	}
	// Rules should be sorted alphabetically.
	if rules[0].Name != "exited" || rules[1].Name != "high_cpu" {
		t.Errorf("rules not sorted: %s, %s", rules[0].Name, rules[1].Name)
	}
	if rules[0].FiringCount != 0 {
		t.Errorf("exited firing count = %d, want 0", rules[0].FiringCount)
	}
	if rules[1].Condition != "host.cpu_percent > 90" {
		t.Errorf("condition = %q, want 'host.cpu_percent > 90'", rules[1].Condition)
	}
	if rules[1].For != 30*time.Second {
		t.Errorf("for = %v, want 30s", rules[1].For)
	}

	// Fire some alerts.
	a.Evaluate(ctx, &MetricSnapshot{
		Host:       &HostMetrics{CPUPercent: 95},
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})

	// high_cpu has for=30s so should be pending, not firing.
	rules = a.QueryRules()
	if rules[0].FiringCount != 1 { // exited:aaa
		t.Errorf("exited firing count = %d, want 1", rules[0].FiringCount)
	}
	if rules[1].FiringCount != 0 { // high_cpu still pending
		t.Errorf("high_cpu firing count = %d, want 0 (still pending)", rules[1].FiringCount)
	}

	// Advance past for-duration.
	now = now.Add(30 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{
		Host:       &HostMetrics{CPUPercent: 95},
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})
	rules = a.QueryRules()
	if rules[1].FiringCount != 1 {
		t.Errorf("high_cpu firing count = %d, want 1", rules[1].FiringCount)
	}

	// Silence a rule and verify.
	a.Silence("exited", 5*time.Minute)
	rules = a.QueryRules()
	if rules[0].SilencedUntil.IsZero() {
		t.Error("exited should be silenced")
	}
	expected := now.Add(5 * time.Minute)
	if !rules[0].SilencedUntil.Equal(expected) {
		t.Errorf("silenced until = %v, want %v", rules[0].SilencedUntil, expected)
	}
	if !rules[1].SilencedUntil.IsZero() {
		t.Error("high_cpu should not be silenced")
	}
}

func TestContainerExitCodeAlert(t *testing.T) {
	alerts := map[string]AlertConfig{
		"nonzero_exit": {
			Condition: "container.exit_code != 0",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Exit code 0 — no fire.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited", ExitCode: 0}},
	})
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("alert rows = %d, want 0", count)
	}

	// Exit code 137 — fires.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited", ExitCode: 137}},
	})
	inst := a.instances["nonzero_exit:aaa"]
	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected firing with exit code 137")
	}

	// Back to exit code 0 — resolves.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "running", ExitCode: 0}},
	})
	inst = a.instances["nonzero_exit:aaa"]
	if inst != nil && inst.state != stateInactive {
		t.Errorf("expected resolved, got state %d", inst.state)
	}
}

func TestLogAlertFiresOnThreshold(t *testing.T) {
	alerts := map[string]AlertConfig{
		"error_spike": {
			Condition: "log.count > 5",
			Match:     "error",
			Window:    Duration{5 * time.Minute},
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Insert 6 error logs for container "aaa".
	var entries []LogEntry
	for i := 0; i < 6; i++ {
		entries = append(entries, LogEntry{
			Timestamp:     now.Add(-time.Duration(i) * time.Second),
			ContainerID:   "aaa",
			ContainerName: "web",
			Stream:        "stderr",
			Message:       "error: something broke",
		})
	}
	s.InsertLogs(ctx, entries)

	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "running"}},
	})

	inst := a.instances["error_spike:aaa"]
	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected error_spike:aaa to be firing")
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE rule_name = 'error_spike'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1", count)
	}
}

func TestLogAlertDoesNotFireBelowThreshold(t *testing.T) {
	alerts := map[string]AlertConfig{
		"error_spike": {
			Condition: "log.count > 5",
			Match:     "error",
			Window:    Duration{5 * time.Minute},
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Insert only 3 error logs.
	s.InsertLogs(ctx, []LogEntry{
		{Timestamp: now, ContainerID: "aaa", ContainerName: "web", Stream: "stderr", Message: "error: one"},
		{Timestamp: now, ContainerID: "aaa", ContainerName: "web", Stream: "stderr", Message: "error: two"},
		{Timestamp: now, ContainerID: "aaa", ContainerName: "web", Stream: "stderr", Message: "error: three"},
	})

	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "running"}},
	})

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("alert rows = %d, want 0 (below threshold)", count)
	}
}

func TestLogAlertPerContainer(t *testing.T) {
	alerts := map[string]AlertConfig{
		"error_spike": {
			Condition: "log.count > 5",
			Match:     "error",
			Window:    Duration{5 * time.Minute},
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// 6 errors for "aaa", 2 for "bbb".
	var entries []LogEntry
	for i := 0; i < 6; i++ {
		entries = append(entries, LogEntry{
			Timestamp: now, ContainerID: "aaa", ContainerName: "web",
			Stream: "stderr", Message: "error: fail",
		})
	}
	entries = append(entries,
		LogEntry{Timestamp: now, ContainerID: "bbb", ContainerName: "api", Stream: "stderr", Message: "error: one"},
		LogEntry{Timestamp: now, ContainerID: "bbb", ContainerName: "api", Stream: "stderr", Message: "error: two"},
	)
	s.InsertLogs(ctx, entries)

	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{
			{ID: "aaa", Name: "web", State: "running"},
			{ID: "bbb", Name: "api", State: "running"},
		},
	})

	// Only "aaa" should fire.
	if inst := a.instances["error_spike:aaa"]; inst == nil || inst.state != stateFiring {
		t.Error("expected error_spike:aaa to be firing")
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1 (only aaa over threshold)", count)
	}
}

func TestLogAlertResolvesWhenCountDrops(t *testing.T) {
	alerts := map[string]AlertConfig{
		"error_spike": {
			Condition: "log.count > 5",
			Match:     "error",
			Window:    Duration{5 * time.Minute},
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Insert 6 error logs.
	var entries []LogEntry
	for i := 0; i < 6; i++ {
		entries = append(entries, LogEntry{
			Timestamp: now, ContainerID: "aaa", ContainerName: "web",
			Stream: "stderr", Message: "error: fail",
		})
	}
	s.InsertLogs(ctx, entries)

	snap := &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "running"}},
	}

	// Fire.
	a.Evaluate(ctx, snap)
	if a.instances["error_spike:aaa"].state != stateFiring {
		t.Fatal("expected firing")
	}

	// Advance past window — logs are now outside the 5m window.
	now = now.Add(10 * time.Minute)
	a.Evaluate(ctx, snap)

	inst := a.instances["error_spike:aaa"]
	if inst != nil && inst.state != stateInactive {
		t.Errorf("expected resolved after window elapsed, got state %d", inst.state)
	}

	var resolvedAt *int64
	if err := s.db.QueryRow("SELECT resolved_at FROM alerts WHERE rule_name = 'error_spike'").Scan(&resolvedAt); err != nil {
		t.Fatal(err)
	}
	if resolvedAt == nil {
		t.Error("resolved_at should be set")
	}
}

func TestLogAlertRegexMatch(t *testing.T) {
	alerts := map[string]AlertConfig{
		"oom": {
			Condition:  "log.count > 0",
			Match:      "OOM|out of memory",
			MatchRegex: true,
			Window:     Duration{10 * time.Minute},
			Severity:   "critical",
			Actions:    []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	s.InsertLogs(ctx, []LogEntry{
		{Timestamp: now, ContainerID: "aaa", ContainerName: "web", Stream: "stderr", Message: "OOM killed process 1234"},
		{Timestamp: now, ContainerID: "bbb", ContainerName: "api", Stream: "stderr", Message: "out of memory allocating 1MB"},
		{Timestamp: now, ContainerID: "ccc", ContainerName: "db", Stream: "stdout", Message: "normal log line"},
	})

	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{
			{ID: "aaa", Name: "web", State: "running"},
			{ID: "bbb", Name: "api", State: "running"},
			{ID: "ccc", Name: "db", State: "running"},
		},
	})

	if inst := a.instances["oom:aaa"]; inst == nil || inst.state != stateFiring {
		t.Error("expected oom:aaa firing")
	}
	if inst := a.instances["oom:bbb"]; inst == nil || inst.state != stateFiring {
		t.Error("expected oom:bbb firing")
	}

	// "ccc" has no matching logs.
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("alert rows = %d, want 2", count)
	}
}

func TestLogAlertWithForDuration(t *testing.T) {
	alerts := map[string]AlertConfig{
		"error_spike": {
			Condition: "log.count > 5",
			Match:     "error",
			Window:    Duration{5 * time.Minute},
			For:       Duration{30 * time.Second},
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Insert 6 error logs.
	var entries []LogEntry
	for i := 0; i < 6; i++ {
		entries = append(entries, LogEntry{
			Timestamp: now, ContainerID: "aaa", ContainerName: "web",
			Stream: "stderr", Message: "error: fail",
		})
	}
	s.InsertLogs(ctx, entries)

	snap := &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "running"}},
	}

	// First eval — should go pending.
	a.Evaluate(ctx, snap)
	if a.instances["error_spike:aaa"].state != statePending {
		t.Fatal("expected pending")
	}

	// 15s later — still pending.
	now = now.Add(15 * time.Second)
	a.Evaluate(ctx, snap)
	if a.instances["error_spike:aaa"].state != statePending {
		t.Fatal("expected still pending at 15s")
	}

	// 30s total — should fire.
	now = now.Add(15 * time.Second)
	a.Evaluate(ctx, snap)
	if a.instances["error_spike:aaa"].state != stateFiring {
		t.Fatal("expected firing at 30s")
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1", count)
	}
}

func TestLogAlertNilContainersDoesNotFalseResolve(t *testing.T) {
	alerts := map[string]AlertConfig{
		"error_spike": {
			Condition: "log.count > 5",
			Match:     "error",
			Window:    Duration{5 * time.Minute},
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Insert 6 error logs and fire.
	var entries []LogEntry
	for i := 0; i < 6; i++ {
		entries = append(entries, LogEntry{
			Timestamp: now, ContainerID: "aaa", ContainerName: "web",
			Stream: "stderr", Message: "error: fail",
		})
	}
	s.InsertLogs(ctx, entries)

	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "running"}},
	})
	if a.instances["error_spike:aaa"].state != stateFiring {
		t.Fatal("expected firing")
	}

	// Nil containers (collection failed) — should NOT resolve.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{})

	if a.instances["error_spike:aaa"].state != stateFiring {
		t.Error("nil containers should not resolve active log alert")
	}
}

func TestLogAlertFireMessage(t *testing.T) {
	alerts := map[string]AlertConfig{
		"error_spike": {
			Condition: "log.count > 5",
			Match:     "error",
			Window:    Duration{5 * time.Minute},
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	var entries []LogEntry
	for i := 0; i < 6; i++ {
		entries = append(entries, LogEntry{
			Timestamp: now, ContainerID: "aaa", ContainerName: "web",
			Stream: "stderr", Message: "error: fail",
		})
	}
	s.InsertLogs(ctx, entries)

	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "running"}},
	})

	// Verify the alert message format.
	var msg string
	if err := s.db.QueryRow("SELECT message FROM alerts WHERE rule_name = 'error_spike'").Scan(&msg); err != nil {
		t.Fatal(err)
	}
	if msg != `[warning] error_spike: log matches for "error" (web)` {
		t.Errorf("message = %q", msg)
	}
}

