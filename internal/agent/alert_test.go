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
		{"host.cpu_percent", "", "", "", 0, "", false, true},                  // too few tokens
		{"host.cpu_percent > 90 extra", "", "", "", 0, "", false, true},       // too many tokens
		{"cpu_percent > 90", "", "", "", 0, "", false, true},                  // no scope.field
		{"host.cpu_percent ~ 90", "", "", "", 0, "", false, true},             // bad operator
		{"host.cpu_percent > abc", "", "", "", 0, "", false, true},            // bad numeric
		{"unknown.cpu_percent > 90", "", "", "", 0, "", false, true},          // bad scope
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

func TestEvalCondition(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		snap      MetricSnapshot
		want      bool
	}{
		{
			"host cpu over",
			"host.cpu_percent > 90",
			MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}},
			true,
		},
		{
			"host cpu under",
			"host.cpu_percent > 90",
			MetricSnapshot{Host: &HostMetrics{CPUPercent: 50}},
			false,
		},
		{
			"host memory over",
			"host.memory_percent > 85",
			MetricSnapshot{Host: &HostMetrics{MemPercent: 90}},
			true,
		},
		{
			"disk over threshold",
			"host.disk_percent > 90",
			MetricSnapshot{Disks: []DiskMetrics{{Mountpoint: "/", Percent: 95}}},
			true,
		},
		{
			"container exited",
			"container.state == 'exited'",
			MetricSnapshot{Containers: []ContainerMetrics{{ID: "abc", State: "exited"}}},
			true,
		},
		{
			"container running not exited",
			"container.state == 'exited'",
			MetricSnapshot{Containers: []ContainerMetrics{{ID: "abc", State: "running"}}},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := parseCondition(tt.condition)
			if err != nil {
				t.Fatal(err)
			}

			// Evaluate the condition directly based on scope.
			var got bool
			switch {
			case cond.Scope == "host" && cond.Field == "disk_percent":
				for _, d := range tt.snap.Disks {
					if compareNum(d.Percent, cond.Op, cond.NumVal) {
						got = true
					}
				}
			case cond.Scope == "host":
				if tt.snap.Host != nil {
					got = compareNum(hostFieldValue(tt.snap.Host, cond.Field), cond.Op, cond.NumVal)
				}
			case cond.Scope == "container":
				for _, c := range tt.snap.Containers {
					if cond.IsStr {
						got = compareStr(containerFieldStr(&c, cond.Field), cond.Op, cond.StrVal)
					} else {
						got = compareNum(containerFieldNum(&c, cond.Field), cond.Op, cond.NumVal)
					}
				}
			}

			if got != tt.want {
				t.Errorf("condition %q matched = %v, want %v", tt.condition, got, tt.want)
			}
		})
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
	inst := a.instances["high_cpu"]
	if inst == nil || inst.state != stateInactive {
		t.Fatal("expected inactive state")
	}

	// CPU at 95% — for=0 so should fire immediately.
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 95}})
	if inst.state != stateFiring {
		t.Fatalf("expected firing, got %d", inst.state)
	}

	// Verify alert row in DB.
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE rule_name = 'high_cpu'").Scan(&count)
	if count != 1 {
		t.Errorf("alert rows = %d, want 1", count)
	}

	// CPU back to 50% — should resolve.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 50}})
	if inst.state != stateInactive {
		t.Fatalf("expected inactive after resolve, got %d", inst.state)
	}

	// Verify resolved_at is set.
	var resolvedAt *int64
	s.db.QueryRow("SELECT resolved_at FROM alerts WHERE rule_name = 'high_cpu'").Scan(&resolvedAt)
	if resolvedAt == nil {
		t.Error("resolved_at should be set")
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
	if inst.state != statePending {
		t.Fatalf("expected pending, got %d", inst.state)
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
	s.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE rule_name = 'high_cpu'").Scan(&count)
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
	if inst.state != statePending {
		t.Fatalf("expected pending, got %d", inst.state)
	}

	// Condition false before for elapsed — back to inactive.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Host: &HostMetrics{CPUPercent: 50}})
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
	if inst, ok := a.instances["exited:bbb"]; !ok || inst.state != stateInactive {
		t.Error("expected exited:bbb to be inactive")
	}

	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count)
	if count != 1 {
		t.Errorf("alert rows = %d, want 1", count)
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
	if inst, ok := a.instances["disk_full:/home"]; !ok || inst.state != stateInactive {
		t.Error("expected disk_full:/home to be inactive")
	}

	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count)
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

	// Container disappears — stale firing instance should be resolved.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{Containers: nil})

	if a.instances["exited:aaa"].state != stateInactive {
		t.Fatal("expected stale instance to be resolved")
	}

	var resolvedAt *int64
	s.db.QueryRow("SELECT resolved_at FROM alerts WHERE instance_key = 'exited:aaa'").Scan(&resolvedAt)
	if resolvedAt == nil {
		t.Error("resolved_at should be set for stale instance")
	}
}

func TestNilSnapshotFieldsSkipped(t *testing.T) {
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

	// All nil — should not panic.
	a.Evaluate(ctx, &MetricSnapshot{})
}
