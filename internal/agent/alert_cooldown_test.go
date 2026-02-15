package agent

import (
	"context"
	"testing"
	"time"
)

func TestCooldownPreventsRefire(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			Cooldown:  Duration{5 * time.Minute},
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
	if a.instances["high_cpu"].state != stateFiring {
		t.Fatal("expected firing")
	}

	// Resolve.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 50}})

	// Re-match within cooldown — should stay inactive.
	now = now.Add(1 * time.Minute)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	inst := a.instances["high_cpu"]
	if inst != nil && inst.state != stateInactive {
		t.Fatalf("expected inactive during cooldown, got %d", inst.state)
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1 (re-fire suppressed)", count)
	}

	// Advance past cooldown — should fire normally.
	now = now.Add(5 * time.Minute)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	inst = a.instances["high_cpu"]
	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected firing after cooldown elapsed")
	}

	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("alert rows = %d, want 2", count)
	}
}

func TestCooldownZeroDisabled(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			Cooldown:  Duration{0}, // explicitly disabled
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Fire, resolve, re-fire immediately.
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 50}})
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})

	inst := a.instances["high_cpu"]
	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected immediate re-fire with cooldown=0")
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("alert rows = %d, want 2", count)
	}
}

func TestCooldownWithForDuration(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			For:       Duration{10 * time.Second},
			Cooldown:  Duration{5 * time.Minute},
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Fire (pending -> firing after for-duration).
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	if a.instances["high_cpu"].state != stateFiring {
		t.Fatal("expected firing")
	}

	// Resolve.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 50}})

	// Re-match within cooldown — should NOT even enter pending.
	now = now.Add(1 * time.Minute)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	inst := a.instances["high_cpu"]
	if inst != nil && inst.state != stateInactive {
		t.Fatalf("expected inactive during cooldown, got %d", inst.state)
	}

	// Past cooldown — should enter pending.
	now = now.Add(5 * time.Minute)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	inst = a.instances["high_cpu"]
	if inst == nil || inst.state != statePending {
		t.Fatal("expected pending after cooldown elapsed")
	}

	// After for-duration — should fire.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	if a.instances["high_cpu"].state != stateFiring {
		t.Fatal("expected firing after for-duration")
	}
}

func TestCooldownPerInstance(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Cooldown:  Duration{5 * time.Minute},
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Fire for "aaa", resolve.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "running"}},
	})

	// "bbb" matches same rule within aaa's cooldown — should fire immediately (fresh instance).
	now = now.Add(1 * time.Minute)
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{
			{ID: "aaa", Name: "web", State: "running"},
			{ID: "bbb", Name: "api", State: "exited"},
		},
	})

	inst := a.instances["exited:bbb"]
	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected exited:bbb firing (cooldown is per-instance)")
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE instance_key = 'exited:bbb'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows for bbb = %d, want 1", count)
	}
}

func TestCooldownResetOnGC(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Cooldown:  Duration{5 * time.Minute},
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Fire for "aaa", resolve.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "running"}},
	})

	// Container disappears — instance gets GC'd (inactive + unseen).
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Containers: []ContainerMetrics{}})
	if _, exists := a.instances["exited:aaa"]; exists {
		t.Fatal("expected instance GC'd")
	}

	// Container reappears within what would have been cooldown — fires immediately
	// because instance was GC'd (fresh instance, zero resolvedAt).
	now = now.Add(1 * time.Minute)
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})

	inst := a.instances["exited:aaa"]
	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected firing (instance was GC'd, cooldown reset)")
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE instance_key = 'exited:aaa'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("alert rows = %d, want 2", count)
	}
}

func TestCooldownViaContainerEvent(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Cooldown:  Duration{5 * time.Minute},
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Fire via event.
	a.EvaluateContainerEvent(ctx, ContainerMetrics{ID: "aaa", Name: "web", State: "exited"})
	a.mu.Lock()
	if a.instances["exited:aaa"].state != stateFiring {
		t.Fatal("expected firing")
	}
	a.mu.Unlock()

	// Resolve via event.
	now = now.Add(10 * time.Second)
	a.EvaluateContainerEvent(ctx, ContainerMetrics{ID: "aaa", Name: "web", State: "running"})

	// Re-match within cooldown via event — should stay inactive.
	now = now.Add(1 * time.Minute)
	a.EvaluateContainerEvent(ctx, ContainerMetrics{ID: "aaa", Name: "web", State: "exited"})

	a.mu.Lock()
	inst := a.instances["exited:aaa"]
	a.mu.Unlock()
	if inst != nil && inst.state != stateInactive {
		t.Fatalf("expected inactive during cooldown, got %d", inst.state)
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE instance_key = 'exited:aaa'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1 (re-fire suppressed by cooldown)", count)
	}
}

func TestCooldownDoesNotAffectFirstFire(t *testing.T) {
	alerts := map[string]AlertConfig{
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			Cooldown:  Duration{5 * time.Minute},
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, s := testAlerter(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// First match — should fire immediately (resolvedAt is zero, cooldown skipped).
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	inst := a.instances["high_cpu"]
	if inst == nil || inst.state != stateFiring {
		t.Fatal("expected firing on first match")
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1", count)
	}
}
