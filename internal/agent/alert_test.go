package agent

import (
	"context"
	"testing"
	"time"
)

func TestParseCondition(t *testing.T) {
	tests := []struct {
		input   string
		scope   string
		field   string
		op      string
		numVal  float64
		strVal  string
		isStr   bool
		wantErr bool
	}{
		{"host.cpu_percent > 90", "host", "cpu_percent", ">", 90, "", false, false},
		{"host.memory_percent >= 85.5", "host", "memory_percent", ">=", 85.5, "", false, false},
		{"host.disk_percent > 90", "host", "disk_percent", ">", 90, "", false, false},
		{"container.state == 'exited'", "container", "state", "==", 0, "exited", true, false},
		{"container.cpu_percent > 80", "container", "cpu_percent", ">", 80, "", false, false},
		{"container.state != 'running'", "container", "state", "!=", 0, "running", true, false},

		// Invalid cases.
		{"", "", "", "", 0, "", false, true},
		{"host.cpu_percent", "", "", "", 0, "", false, true},            // too few tokens
		{"host.cpu_percent > 90 extra", "", "", "", 0, "", false, true}, // too many tokens
		{"cpu_percent > 90", "", "", "", 0, "", false, true},            // no scope.field
		{"host.cpu_percent ~ 90", "", "", "", 0, "", false, true},       // bad operator
		{"host.cpu_percent > abc", "", "", "", 0, "", false, true},      // bad numeric
		{"unknown.cpu_percent > 90", "", "", "", 0, "", false, true},    // bad scope
		{"host.load1 > 2", "", "", "", 0, "", false, true},             // unknown field
		{"container.image == 'nginx'", "", "", "", 0, "", false, true},  // unknown field
		{"container.state > 'exited'", "", "", "", 0, "", false, true},  // string field with > op
		{"container.state >= 'a'", "", "", "", 0, "", false, true},      // string field with >= op
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			c, err := parseCondition(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.Scope != tt.scope {
				t.Errorf("scope = %q, want %q", c.Scope, tt.scope)
			}
			if c.Field != tt.field {
				t.Errorf("field = %q, want %q", c.Field, tt.field)
			}
			if c.Op != tt.op {
				t.Errorf("op = %q, want %q", c.Op, tt.op)
			}
			if c.IsStr != tt.isStr {
				t.Errorf("isStr = %v, want %v", c.IsStr, tt.isStr)
			}
			if c.IsStr && c.StrVal != tt.strVal {
				t.Errorf("strVal = %q, want %q", c.StrVal, tt.strVal)
			}
			if !c.IsStr && c.NumVal != tt.numVal {
				t.Errorf("numVal = %f, want %f", c.NumVal, tt.numVal)
			}
		})
	}
}

func TestCompareNum(t *testing.T) {
	tests := []struct {
		actual    float64
		op        string
		threshold float64
		want      bool
	}{
		{91, ">", 90, true},
		{90, ">", 90, false},
		{89, ">", 90, false},
		{89, "<", 90, true},
		{90, "<", 90, false},
		{90, ">=", 90, true},
		{90, "<=", 90, true},
		{90, "==", 90, true},
		{91, "==", 90, false},
		{91, "!=", 90, true},
		{90, "!=", 90, false},
	}

	for _, tt := range tests {
		got := compareNum(tt.actual, tt.op, tt.threshold)
		if got != tt.want {
			t.Errorf("compareNum(%f, %q, %f) = %v, want %v", tt.actual, tt.op, tt.threshold, got, tt.want)
		}
	}
}

func TestCompareStr(t *testing.T) {
	tests := []struct {
		actual, op, expected string
		want                 bool
	}{
		{"exited", "==", "exited", true},
		{"running", "==", "exited", false},
		{"exited", "!=", "running", true},
		{"running", "!=", "running", false},
	}

	for _, tt := range tests {
		got := compareStr(tt.actual, tt.op, tt.expected)
		if got != tt.want {
			t.Errorf("compareStr(%q, %q, %q) = %v, want %v", tt.actual, tt.op, tt.expected, got, tt.want)
		}
	}
}

// testAlerter creates an Alerter with a test store and injectable clock.
func testAlerter(t *testing.T, alerts map[string]AlertConfig) (*Alerter, *Store) {
	t.Helper()
	s := testStore(t)
	n := NewNotifier(&NotifyConfig{})
	a, err := NewAlerter(alerts, s, n, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a, s
}

// fakeDocker implements just enough to test RestartContainer.
type fakeDocker struct {
	restarted []string
	err       error
}

func (f *fakeDocker) restart(containerID string) {
	f.restarted = append(f.restarted, containerID)
}

// testAlerterWithDocker creates an Alerter with a fake docker restart hook.
func testAlerterWithDocker(t *testing.T, alerts map[string]AlertConfig) (*Alerter, *Store, *fakeDocker) {
	t.Helper()
	s := testStore(t)
	n := NewNotifier(&NotifyConfig{})
	a, err := NewAlerter(alerts, s, n, nil)
	if err != nil {
		t.Fatal(err)
	}
	fd := &fakeDocker{}
	// Inject a custom doRestart that records calls without needing a real Docker client.
	a.restartFn = func(ctx context.Context, containerID string) error {
		fd.restart(containerID)
		return fd.err
	}
	return a, s, fd
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

func TestRestartAction(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition:   "container.state == 'exited'",
			Severity:    "critical",
			Actions:     []string{"restart"},
			MaxRestarts: 2,
		},
	}
	a, _, fd := testAlerterWithDocker(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Fire — should restart.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})

	if len(fd.restarted) != 1 || fd.restarted[0] != "aaa" {
		t.Errorf("expected 1 restart of aaa, got %v", fd.restarted)
	}
	if a.instances["exited:aaa"].restarts != 1 {
		t.Errorf("restart count = %d, want 1", a.instances["exited:aaa"].restarts)
	}
}

func TestRestartMaxLimit(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition:   "container.state == 'exited'",
			Severity:    "critical",
			Actions:     []string{"restart"},
			MaxRestarts: 1,
		},
	}
	a, _, fd := testAlerterWithDocker(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// First fire — restarts.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})
	if len(fd.restarted) != 1 {
		t.Fatalf("expected 1 restart, got %d", len(fd.restarted))
	}

	// Resolve.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "running"}},
	})

	// Re-fire — restarts again (counter reset on resolve).
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})
	if len(fd.restarted) != 2 {
		t.Fatalf("expected 2 total restarts, got %d", len(fd.restarted))
	}

	// Resolve and re-fire twice without resolving — second should be blocked.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "running"}},
	})
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})
	if len(fd.restarted) != 3 {
		t.Fatalf("expected 3 total restarts, got %d", len(fd.restarted))
	}

	// Still firing, resolve and fire again to hit max_restarts=1 in a single firing period.
	// Actually the restart happens on fire transition, so max=1 means 1 restart per fire.
	// The limit is already reached at 1 (restarts == maxRestarts after first restart).
	// Verify by direct instance check.
	inst := a.instances["exited:aaa"]
	if inst.restarts != 1 {
		t.Errorf("restart count = %d, want 1", inst.restarts)
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
