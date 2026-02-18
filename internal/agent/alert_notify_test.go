package agent

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recordingChannel records notification subjects for test verification.
type recordingChannel struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingChannel) Send(_ context.Context, subject, _ string) error {
	r.mu.Lock()
	r.calls = append(r.calls, subject)
	r.mu.Unlock()
	return nil
}

func (r *recordingChannel) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

func testAlerterWithRecorder(t *testing.T, alerts map[string]AlertConfig) (*Alerter, *Store, *recordingChannel) {
	t.Helper()
	s := testStore(t)
	rec := &recordingChannel{}
	n := &Notifier{
		channels: []Channel{rec},
		queue:    make(chan notification, 64),
	}
	n.wg.Add(1)
	go n.run()
	t.Cleanup(func() { n.Stop() })
	a, err := NewAlerter(alerts, s, n)
	if err != nil {
		t.Fatal(err)
	}
	return a, s, rec
}

func TestNotifyCooldownSuppresses(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition:      "container.state == 'exited'",
			Cooldown:       Duration{0},
			NotifyCooldown: Duration{5 * time.Minute},
			Severity:       "critical",
			Actions:        []string{"notify"},
		},
	}
	a, s, rec := testAlerterWithRecorder(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Fire "aaa" — notification sent.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})
	a.notifier.Flush()
	if calls := rec.Calls(); len(calls) != 1 {
		t.Fatalf("notifications = %d, want 1", len(calls))
	}

	// Fire "bbb" of same rule within window — DB row created, notification suppressed.
	now = now.Add(10 * time.Second)
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{
			{ID: "aaa", Name: "web", State: "exited"},
			{ID: "bbb", Name: "api", State: "exited"},
		},
	})
	a.notifier.Flush()
	if calls := rec.Calls(); len(calls) != 1 {
		t.Fatalf("notifications = %d, want 1 (suppressed)", len(calls))
	}

	// Verify DB row was still created.
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("alert rows = %d, want 2 (both fired in DB)", count)
	}

	// Advance past window — "ccc" fires and notifies.
	now = now.Add(5 * time.Minute)
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{
			{ID: "aaa", Name: "web", State: "exited"},
			{ID: "bbb", Name: "api", State: "exited"},
			{ID: "ccc", Name: "db", State: "exited"},
		},
	})
	a.notifier.Flush()
	if calls := rec.Calls(); len(calls) != 2 {
		t.Fatalf("notifications = %d, want 2", len(calls))
	}
}

func TestNotifyCooldownZeroDisabled(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition:      "container.state == 'exited'",
			Cooldown:       Duration{0},
			NotifyCooldown: Duration{0}, // explicitly disabled
			Severity:       "critical",
			Actions:        []string{"notify"},
		},
	}
	a, _, rec := testAlerterWithRecorder(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Fire two containers — both should notify independently.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{
			{ID: "aaa", Name: "web", State: "exited"},
			{ID: "bbb", Name: "api", State: "exited"},
		},
	})
	a.notifier.Flush()
	if calls := rec.Calls(); len(calls) != 2 {
		t.Fatalf("notifications = %d, want 2 (cooldown disabled)", len(calls))
	}
}

func TestNotifyCooldownPerRule(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition:      "container.state == 'exited'",
			Cooldown:       Duration{0},
			NotifyCooldown: Duration{5 * time.Minute},
			Severity:       "critical",
			Actions:        []string{"notify"},
		},
		"unhealthy": {
			Condition:      "container.health == 'unhealthy'",
			Cooldown:       Duration{0},
			NotifyCooldown: Duration{5 * time.Minute},
			Severity:       "warning",
			Actions:        []string{"notify"},
		},
	}
	a, _, rec := testAlerterWithRecorder(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Fire one instance of each rule — 2 notifications (independent timers).
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{
			{ID: "aaa", Name: "web", State: "exited", Health: "unhealthy"},
		},
	})
	a.notifier.Flush()
	if calls := rec.Calls(); len(calls) != 2 {
		t.Fatalf("notifications = %d, want 2 (one per rule)", len(calls))
	}
}

func TestNotifyCooldownDoesNotAffectAlertCreation(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition:      "container.state == 'exited'",
			Cooldown:       Duration{0},
			NotifyCooldown: Duration{5 * time.Minute},
			Severity:       "critical",
			Actions:        []string{"notify"},
		},
	}
	a, s, _ := testAlerterWithRecorder(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	var callbacks []string
	a.onStateChange = func(alert *Alert, state string) {
		callbacks = append(callbacks, state+":"+alert.InstanceKey)
	}

	// Fire two containers — both create DB rows and trigger onStateChange,
	// even though second notification is suppressed.
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{
			{ID: "aaa", Name: "web", State: "exited"},
			{ID: "bbb", Name: "api", State: "exited"},
		},
	})

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("alert rows = %d, want 2", count)
	}
	if len(callbacks) != 2 {
		t.Errorf("callbacks = %d, want 2", len(callbacks))
	}
}

func TestNotifyCooldownSilenceInteraction(t *testing.T) {
	alerts := map[string]AlertConfig{
		"exited": {
			Condition:      "container.state == 'exited'",
			Cooldown:       Duration{0},
			NotifyCooldown: Duration{5 * time.Minute},
			Severity:       "critical",
			Actions:        []string{"notify"},
		},
	}
	a, _, rec := testAlerterWithRecorder(t, alerts)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Silence rule, fire "aaa" — silenced, no notification, lastNotified NOT set.
	a.Silence("exited", 1*time.Minute)
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})
	a.notifier.Flush()
	if calls := rec.Calls(); len(calls) != 0 {
		t.Fatalf("notifications = %d, want 0 (silenced)", len(calls))
	}

	// Verify lastNotified was NOT set by silenced fire.
	a.mu.Lock()
	_, hasLast := a.lastNotified["exited"]
	a.mu.Unlock()
	if hasLast {
		t.Fatal("lastNotified should not be set when silenced")
	}

	// Unsilence (advance past silence duration).
	now = now.Add(2 * time.Minute)

	// Fire "bbb" — should notify (silence expired, no prior lastNotified to cooldown against).
	a.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{
			{ID: "aaa", Name: "web", State: "exited"},
			{ID: "bbb", Name: "api", State: "exited"},
		},
	})
	a.notifier.Flush()
	if calls := rec.Calls(); len(calls) != 1 {
		t.Fatalf("notifications = %d, want 1", len(calls))
	}
}
