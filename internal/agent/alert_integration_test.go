package agent

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

// --- Test helpers ---

// testAlertPipeline wires together a real Store, Alerter, Hub, and EventWatcher
// for integration testing. Returns all components so tests can drive events and
// verify outcomes across the full pipeline.
type alertPipeline struct {
	store   *Store
	alerter *Alerter
	hub     *Hub
	events  *EventWatcher
	src     *fakeEventSource
}

func newAlertPipeline(t *testing.T, alerts map[string]AlertConfig) *alertPipeline {
	t.Helper()
	s := testStore(t)
	n := NewNotifier(&NotifyConfig{})
	alerter, err := NewAlerter(alerts, s, n)
	if err != nil {
		t.Fatal(err)
	}
	alerter.now = func() time.Time { return time.Now() }

	hub := NewHub()
	dc := &DockerCollector{
		prevCPU:         make(map[string]cpuPrev),
		tracked:         map[string]bool{"web": true, "api": true, "db": true},
		trackedProjects: make(map[string]bool),
	}

	ew := &EventWatcher{
		docker: dc,
		hub:    hub,
		done:   make(chan struct{}),
	}
	src := newFakeEventSource()
	ew.eventsFn = src.fn
	ew.SetAlerter(alerter)

	alerter.onStateChange = func(alert *Alert, state string) {
		event := &protocol.AlertEvent{
			ID:          alert.ID,
			RuleName:    alert.RuleName,
			Severity:    alert.Severity,
			Condition:   alert.Condition,
			InstanceKey: alert.InstanceKey,
			FiredAt:     alert.FiredAt.Unix(),
			Message:     alert.Message,
			State:       state,
		}
		if alert.ResolvedAt != nil {
			event.ResolvedAt = alert.ResolvedAt.Unix()
		}
		hub.Publish(TopicAlerts, event)
	}

	return &alertPipeline{
		store:   s,
		alerter: alerter,
		hub:     hub,
		events:  ew,
		src:     src,
	}
}

// --- 1. Event → Alerter → Store → Hub: full pipeline ---

// TestPipelineEventDieFiresAlertAndPersists verifies that a Docker "die" event
// flows through EventWatcher → Alerter → Store (alert row) → Hub (alert event).
func TestPipelineEventDieFiresAlertAndPersists(t *testing.T) {
	p := newAlertPipeline(t, map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	})

	// Subscribe to alerts hub for the "firing" event.
	_, alertCh := p.hub.Subscribe(TopicAlerts)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.events.watch(ctx)

	// Push a die event through the fake Docker events stream.
	p.src.msgCh <- events.Message{
		Action: events.ActionDie,
		Actor: events.Actor{
			ID:         "abc123",
			Attributes: map[string]string{"name": "web", "image": "nginx"},
		},
		Time: time.Now().Unix(),
	}

	// Verify hub receives the alert event.
	select {
	case msg := <-alertCh:
		event, ok := msg.(*protocol.AlertEvent)
		if !ok {
			t.Fatalf("unexpected message type: %T", msg)
		}
		if event.State != "firing" {
			t.Errorf("state = %q, want firing", event.State)
		}
		if event.RuleName != "exited" {
			t.Errorf("rule_name = %q, want exited", event.RuleName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for alert event on hub")
	}

	// Verify alert row persisted in DB.
	var count int
	if err := p.store.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE rule_name = 'exited'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("alert rows = %d, want 1", count)
	}
}

// TestPipelineHealthStatusFiresAlert verifies that health_status events flow
// through the full pipeline: EventWatcher → Alerter → Store → Hub.
func TestPipelineHealthStatusFiresAlert(t *testing.T) {
	p := newAlertPipeline(t, map[string]AlertConfig{
		"unhealthy": {
			Condition: "container.health == 'unhealthy'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	})

	_, alertCh := p.hub.Subscribe(TopicAlerts)
	_, containerCh := p.hub.Subscribe(TopicContainers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.events.watch(ctx)

	p.src.msgCh <- events.Message{
		Action: "health_status: unhealthy",
		Actor: events.Actor{
			ID:         "abc123",
			Attributes: map[string]string{"name": "web", "image": "nginx"},
		},
		Time: time.Now().Unix(),
	}

	// Verify container event carries health field.
	select {
	case msg := <-containerCh:
		event := msg.(*protocol.ContainerEvent)
		if event.Health != "unhealthy" {
			t.Errorf("container event health = %q, want unhealthy", event.Health)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for container event")
	}

	// Verify alert fires.
	select {
	case msg := <-alertCh:
		event := msg.(*protocol.AlertEvent)
		if event.State != "firing" {
			t.Errorf("alert state = %q, want firing", event.State)
		}
		if event.RuleName != "unhealthy" {
			t.Errorf("rule_name = %q, want unhealthy", event.RuleName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for alert event")
	}

	// Verify persisted.
	alerts, err := p.store.QueryFiringAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].RuleName != "unhealthy" {
		t.Errorf("firing alerts = %v, want 1 unhealthy", alerts)
	}
}

// TestPipelineEventResolvesExistingAlert verifies that a "start" event resolves
// a previously firing "exited" alert through the full pipeline.
func TestPipelineEventResolvesExistingAlert(t *testing.T) {
	p := newAlertPipeline(t, map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	})

	_, alertCh := p.hub.Subscribe(TopicAlerts)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.events.watch(ctx)

	// Fire via die event.
	p.src.msgCh <- events.Message{
		Action: events.ActionDie,
		Actor: events.Actor{
			ID:         "abc123",
			Attributes: map[string]string{"name": "web", "image": "nginx"},
		},
		Time: time.Now().Unix(),
	}

	// Wait for firing event.
	select {
	case msg := <-alertCh:
		if msg.(*protocol.AlertEvent).State != "firing" {
			t.Fatalf("expected firing, got %s", msg.(*protocol.AlertEvent).State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for firing event")
	}

	// Resolve via start event.
	p.src.msgCh <- events.Message{
		Action: events.ActionStart,
		Actor: events.Actor{
			ID:         "abc123",
			Attributes: map[string]string{"name": "web", "image": "nginx"},
		},
		Time: time.Now().Unix(),
	}

	// Wait for resolved event.
	select {
	case msg := <-alertCh:
		if msg.(*protocol.AlertEvent).State != "resolved" {
			t.Fatalf("expected resolved, got %s", msg.(*protocol.AlertEvent).State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for resolved event")
	}

	// No firing alerts remain in DB.
	alerts, err := p.store.QueryFiringAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 0 {
		t.Errorf("firing alerts = %d, want 0", len(alerts))
	}
}

// TestPipelineNumericAlertSurvivesEvent verifies that a firing numeric alert
// (e.g., cpu_percent > 80) is NOT affected by Docker state events, which carry
// zero-value metrics.
func TestPipelineNumericAlertSurvivesEvent(t *testing.T) {
	p := newAlertPipeline(t, map[string]AlertConfig{
		"high_cpu": {
			Condition: "container.cpu_percent > 80",
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	})

	// Fire the numeric alert via regular Evaluate (simulating a collect cycle).
	p.alerter.Evaluate(context.Background(), &MetricSnapshot{
		Containers: []ContainerMetrics{
			{ID: "abc123", Name: "web", State: "running", CPUPercent: 95},
		},
	})
	if p.alerter.instances["high_cpu:abc123"].state != stateFiring {
		t.Fatal("precondition: high_cpu:abc123 not firing")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.events.watch(ctx)

	// Send a die event — should fire exited alert but NOT resolve cpu alert.
	p.src.msgCh <- events.Message{
		Action: events.ActionDie,
		Actor: events.Actor{
			ID:         "abc123",
			Attributes: map[string]string{"name": "web", "image": "nginx"},
		},
		Time: time.Now().Unix(),
	}

	time.Sleep(100 * time.Millisecond)

	p.alerter.mu.Lock()
	cpuInst := p.alerter.instances["high_cpu:abc123"]
	exitInst := p.alerter.instances["exited:abc123"]
	p.alerter.mu.Unlock()

	if cpuInst == nil || cpuInst.state != stateFiring {
		t.Error("high_cpu:abc123 should still be firing after die event")
	}
	if exitInst == nil || exitInst.state != stateFiring {
		t.Error("exited:abc123 should be firing after die event")
	}
}

// --- 2. Agent startup with dirty DB state ---

// TestStartupAdoptsFiringAlerts simulates an agent restart where the previous
// process left unresolved alerts. Alerts matching current rules are adopted
// into the alerter (stay firing, same DB ID). Alerts with no matching rule
// are resolved.
func TestStartupAdoptsFiringAlerts(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Phase 1: simulate old agent that crashes with firing alerts.
	s1, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ts := time.Now().Add(-time.Hour)
	id1, _ := s1.InsertAlert(ctx, &Alert{
		RuleName: "exited", Severity: "critical", Condition: "container.state == 'exited'",
		InstanceKey: "exited:aaa", FiredAt: ts, Message: "orphan 1",
	})
	s1.InsertAlert(ctx, &Alert{
		RuleName: "removed_rule", Severity: "critical", Condition: "container.health == 'unhealthy'",
		InstanceKey: "removed_rule:bbb", FiredAt: ts, Message: "orphan 2",
	})
	s1.Close()

	// Phase 2: new agent opens the same DB with only the "exited" rule.
	s2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	n := NewNotifier(&NotifyConfig{})
	alerter, err := NewAlerter(map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}, s2, n)
	if err != nil {
		t.Fatal(err)
	}

	if err := alerter.AdoptFiring(ctx); err != nil {
		t.Fatal(err)
	}

	// "exited:aaa" should be adopted (still firing, same DB ID).
	inst := alerter.instances["exited:aaa"]
	if inst == nil {
		t.Fatal("exited:aaa not adopted into instances map")
	}
	if inst.state != stateFiring {
		t.Errorf("exited:aaa state = %d, want stateFiring", inst.state)
	}
	if inst.dbID != id1 {
		t.Errorf("exited:aaa dbID = %d, want %d (same row)", inst.dbID, id1)
	}

	// "removed_rule:bbb" should be resolved (no matching rule).
	firing, err := s2.QueryFiringAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(firing) != 1 {
		t.Errorf("firing alerts = %d, want 1 (only adopted)", len(firing))
	}
	if len(firing) == 1 && firing[0].InstanceKey != "exited:aaa" {
		t.Errorf("firing alert key = %q, want exited:aaa", firing[0].InstanceKey)
	}

	// Both rows still exist (resolved, not deleted).
	var total int
	if err := s2.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("total alerts = %d, want 2", total)
	}
}

// TestStartupAdoptThenEvaluateNoDuplicate verifies that after adopting a firing
// alert, subsequent Evaluate cycles do NOT create a new row — the adopted
// instance stays firing with the same DB ID.
func TestStartupAdoptThenEvaluateNoDuplicate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Phase 1: old agent left an unresolved "exited" alert.
	s1, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	origID, _ := s1.InsertAlert(ctx, &Alert{
		RuleName: "exited", Severity: "critical", Condition: "container.state == 'exited'",
		InstanceKey: "exited:aaa", FiredAt: time.Now().Add(-time.Hour), Message: "old",
	})
	s1.Close()

	// Phase 2: new agent adopts, then evaluates the same condition.
	s2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	n := NewNotifier(&NotifyConfig{})
	alerter, err := NewAlerter(map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}, s2, n)
	if err != nil {
		t.Fatal(err)
	}

	if err := alerter.AdoptFiring(ctx); err != nil {
		t.Fatal(err)
	}

	// Evaluate with the same condition still true — should NOT re-fire.
	alerter.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})

	// Still exactly 1 firing alert, same DB row.
	firing, err := s2.QueryFiringAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(firing) != 1 {
		t.Fatalf("firing alerts = %d, want 1", len(firing))
	}
	if firing[0].ID != origID {
		t.Errorf("firing alert ID = %d, want %d (same row, not re-fired)", firing[0].ID, origID)
	}

	// Total rows should be 1 (no duplicate).
	var total int
	if err := s2.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("total alerts = %d, want 1 (adopted, no duplicate)", total)
	}
}

// --- 3. Socket subscribe snapshot ---

// TestSocketSubscribeAlertsSendsSnapshot verifies that subscribing to alerts
// immediately delivers all currently firing alerts from the DB.
func TestSocketSubscribeAlertsSendsSnapshot(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Pre-populate with firing alerts.
	s.InsertAlert(ctx, &Alert{
		RuleName: "exited", Severity: "critical", Condition: "container.state == 'exited'",
		InstanceKey: "exited:aaa", FiredAt: time.Now(), Message: "firing 1",
	})
	s.InsertAlert(ctx, &Alert{
		RuleName: "unhealthy", Severity: "critical", Condition: "container.health == 'unhealthy'",
		InstanceKey: "unhealthy:bbb", FiredAt: time.Now(), Message: "firing 2",
	})
	// A resolved alert — should NOT be in the snapshot.
	id3, _ := s.InsertAlert(ctx, &Alert{
		RuleName: "resolved", Severity: "warning", Condition: "test",
		InstanceKey: "resolved:ccc", FiredAt: time.Now(), Message: "resolved",
	})
	s.ResolveAlert(ctx, id3, time.Now())

	_, _, path := testSocketServer(t, s)
	conn := dial(t, path)

	// Subscribe to alerts.
	env := protocol.NewEnvelopeNoBody(protocol.TypeSubscribeAlerts, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	// Read snapshot messages.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	received := map[string]bool{}
	for i := 0; i < 2; i++ {
		msg, err := protocol.ReadMsg(conn)
		if err != nil {
			t.Fatalf("reading snapshot msg %d: %v", i, err)
		}
		if msg.Type != protocol.TypeAlertEvent {
			t.Fatalf("msg %d type = %q, want alert:event", i, msg.Type)
		}
		var event protocol.AlertEvent
		if err := protocol.DecodeBody(msg.Body, &event); err != nil {
			t.Fatal(err)
		}
		if event.State != "firing" {
			t.Errorf("snapshot alert state = %q, want firing", event.State)
		}
		received[event.RuleName] = true
	}

	if !received["exited"] || !received["unhealthy"] {
		t.Errorf("snapshot rules = %v, want exited and unhealthy", received)
	}
	if received["resolved"] {
		t.Error("resolved alert should not be in snapshot")
	}
}

// TestSocketSubscribeAlertsSnapshotThenStream verifies that after receiving the
// snapshot, the client also receives new streaming alerts.
func TestSocketSubscribeAlertsSnapshotThenStream(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// One pre-existing firing alert.
	s.InsertAlert(ctx, &Alert{
		RuleName: "existing", Severity: "critical", Condition: "test",
		InstanceKey: "existing:aaa", FiredAt: time.Now(), Message: "pre-existing",
	})

	_, hub, path := testSocketServer(t, s)
	conn := dial(t, path)

	// Subscribe.
	env := protocol.NewEnvelopeNoBody(protocol.TypeSubscribeAlerts, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	// Read snapshot (1 alert).
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	msg, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	var snapEvent protocol.AlertEvent
	protocol.DecodeBody(msg.Body, &snapEvent)
	if snapEvent.RuleName != "existing" {
		t.Errorf("snapshot rule = %q, want existing", snapEvent.RuleName)
	}

	// Now publish a new alert via hub (simulating alerter onStateChange).
	hub.Publish(TopicAlerts, &protocol.AlertEvent{
		ID: 99, RuleName: "new_alert", State: "firing",
	})

	// Read the streamed alert.
	msg, err = protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	var streamEvent protocol.AlertEvent
	protocol.DecodeBody(msg.Body, &streamEvent)
	if streamEvent.RuleName != "new_alert" {
		t.Errorf("stream rule = %q, want new_alert", streamEvent.RuleName)
	}
}

// --- 4. Condition coverage: every alert condition type through full pipeline ---

// TestCollectPipelineAllHostConditions verifies that every host alert condition
// type fires and resolves correctly through Alerter.Evaluate + Store.
func TestCollectPipelineAllHostConditions(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		fire      *HostMetrics
		resolve   *HostMetrics
	}{
		{
			name:      "cpu_percent",
			condition: "host.cpu_percent > 90",
			fire:      &HostMetrics{CPUPercent: 95},
			resolve:   &HostMetrics{CPUPercent: 50},
		},
		{
			name:      "memory_percent",
			condition: "host.memory_percent > 90",
			fire:      &HostMetrics{MemPercent: 95},
			resolve:   &HostMetrics{MemPercent: 50},
		},
		{
			name:      "load1",
			condition: "host.load1 > 4",
			fire:      &HostMetrics{Load1: 5},
			resolve:   &HostMetrics{Load1: 2},
		},
		{
			name:      "load5",
			condition: "host.load5 > 4",
			fire:      &HostMetrics{Load5: 5},
			resolve:   &HostMetrics{Load5: 2},
		},
		{
			name:      "load15",
			condition: "host.load15 > 4",
			fire:      &HostMetrics{Load15: 5},
			resolve:   &HostMetrics{Load15: 2},
		},
		{
			name:      "swap_percent",
			condition: "host.swap_percent > 80",
			fire:      &HostMetrics{SwapTotal: 1000, SwapUsed: 900},
			resolve:   &HostMetrics{SwapTotal: 1000, SwapUsed: 100},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testStore(t)
			n := NewNotifier(&NotifyConfig{})
			alerter, err := NewAlerter(map[string]AlertConfig{
				"rule": {Condition: tt.condition, Severity: "warning", Actions: []string{"notify"}},
			}, s, n)
			if err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()
			now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
			alerter.now = func() time.Time { return now }

			// Fire.
			alerter.Evaluate(ctx, &MetricSnapshot{Host: tt.fire})

			firing, err := s.QueryFiringAlerts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(firing) != 1 {
				t.Fatalf("fire: got %d firing alerts, want 1", len(firing))
			}

			// Resolve.
			now = now.Add(10 * time.Second)
			alerter.Evaluate(ctx, &MetricSnapshot{Host: tt.resolve})

			firing, err = s.QueryFiringAlerts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(firing) != 0 {
				t.Fatalf("resolve: got %d firing alerts, want 0", len(firing))
			}
		})
	}
}

// TestCollectPipelineDiskPercent tests disk_percent alerts through the full
// Evaluate + Store pipeline (per-mountpoint keying).
func TestCollectPipelineDiskPercent(t *testing.T) {
	s := testStore(t)
	n := NewNotifier(&NotifyConfig{})
	alerter, err := NewAlerter(map[string]AlertConfig{
		"disk_full": {Condition: "host.disk_percent > 90", Severity: "warning", Actions: []string{"notify"}},
	}, s, n)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	alerter.now = func() time.Time { return now }

	// Two mountpoints, only / exceeds threshold.
	alerter.Evaluate(ctx, &MetricSnapshot{
		Disks: []DiskMetrics{
			{Mountpoint: "/", Percent: 95},
			{Mountpoint: "/home", Percent: 40},
		},
	})

	firing, err := s.QueryFiringAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(firing) != 1 {
		t.Fatalf("got %d firing, want 1", len(firing))
	}
	if firing[0].InstanceKey != "disk_full:/" {
		t.Errorf("instance_key = %q, want disk_full:/", firing[0].InstanceKey)
	}
}

// TestCollectPipelineAllContainerConditions verifies every container alert
// condition type through Alerter.Evaluate + Store.
func TestCollectPipelineAllContainerConditions(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		fire      ContainerMetrics
		resolve   ContainerMetrics
	}{
		{
			name:      "cpu_percent",
			condition: "container.cpu_percent > 80",
			fire:      ContainerMetrics{ID: "aaa", Name: "web", State: "running", CPUPercent: 95},
			resolve:   ContainerMetrics{ID: "aaa", Name: "web", State: "running", CPUPercent: 50},
		},
		{
			name:      "memory_percent",
			condition: "container.memory_percent > 80",
			fire:      ContainerMetrics{ID: "aaa", Name: "web", State: "running", MemPercent: 95},
			resolve:   ContainerMetrics{ID: "aaa", Name: "web", State: "running", MemPercent: 50},
		},
		{
			name:      "state_exited",
			condition: "container.state == 'exited'",
			fire:      ContainerMetrics{ID: "aaa", Name: "web", State: "exited"},
			resolve:   ContainerMetrics{ID: "aaa", Name: "web", State: "running"},
		},
		{
			name:      "health_unhealthy",
			condition: "container.health == 'unhealthy'",
			fire:      ContainerMetrics{ID: "aaa", Name: "web", Health: "unhealthy"},
			resolve:   ContainerMetrics{ID: "aaa", Name: "web", Health: "healthy"},
		},
		{
			name:      "restart_count",
			condition: "container.restart_count > 3",
			fire:      ContainerMetrics{ID: "aaa", Name: "web", RestartCount: 5},
			resolve:   ContainerMetrics{ID: "aaa", Name: "web", RestartCount: 0},
		},
		{
			name:      "exit_code",
			condition: "container.exit_code != 0",
			fire:      ContainerMetrics{ID: "aaa", Name: "web", ExitCode: 137},
			resolve:   ContainerMetrics{ID: "aaa", Name: "web", ExitCode: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testStore(t)
			n := NewNotifier(&NotifyConfig{})
			alerter, err := NewAlerter(map[string]AlertConfig{
				"rule": {Condition: tt.condition, Severity: "warning", Actions: []string{"notify"}},
			}, s, n)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
			alerter.now = func() time.Time { return now }

			// Fire.
			alerter.Evaluate(ctx, &MetricSnapshot{Containers: []ContainerMetrics{tt.fire}})
			firing, err := s.QueryFiringAlerts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(firing) != 1 {
				t.Fatalf("fire: got %d firing alerts, want 1", len(firing))
			}

			// Resolve.
			now = now.Add(10 * time.Second)
			alerter.Evaluate(ctx, &MetricSnapshot{Containers: []ContainerMetrics{tt.resolve}})
			firing, err = s.QueryFiringAlerts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(firing) != 0 {
				t.Fatalf("resolve: got %d firing alerts, want 0", len(firing))
			}
		})
	}
}

// TestEventPipelineAllStringConditions verifies that every string-type container
// condition fires correctly through the event-driven path (EventWatcher → Alerter).
func TestEventPipelineAllStringConditions(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		event     events.Message
	}{
		{
			name:      "state_exited_via_die",
			condition: "container.state == 'exited'",
			event: events.Message{
				Action: events.ActionDie,
				Actor:  events.Actor{ID: "abc123", Attributes: map[string]string{"name": "web", "image": "nginx"}},
				Time:   time.Now().Unix(),
			},
		},
		{
			name:      "state_paused",
			condition: "container.state == 'paused'",
			event: events.Message{
				Action: events.ActionPause,
				Actor:  events.Actor{ID: "abc123", Attributes: map[string]string{"name": "web", "image": "nginx"}},
				Time:   time.Now().Unix(),
			},
		},
		{
			name:      "health_unhealthy",
			condition: "container.health == 'unhealthy'",
			event: events.Message{
				Action: "health_status: unhealthy",
				Actor:  events.Actor{ID: "abc123", Attributes: map[string]string{"name": "web", "image": "nginx"}},
				Time:   time.Now().Unix(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newAlertPipeline(t, map[string]AlertConfig{
				"rule": {Condition: tt.condition, Severity: "critical", Actions: []string{"notify"}},
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go p.events.watch(ctx)

			p.src.msgCh <- tt.event
			time.Sleep(100 * time.Millisecond)

			firing, err := p.store.QueryFiringAlerts(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(firing) != 1 {
				t.Fatalf("got %d firing alerts, want 1", len(firing))
			}
		})
	}
}

// --- 5. Shutdown resolves alerts ---

// TestShutdownResolvesAlerts verifies that ResolveAll on shutdown leaves no
// firing alerts in the DB.
func TestShutdownResolvesAlerts(t *testing.T) {
	s := testStore(t)
	n := NewNotifier(&NotifyConfig{})
	alerter, err := NewAlerter(map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
		"high_cpu": {
			Condition: "host.cpu_percent > 90",
			Severity:  "warning",
			Actions:   []string{"notify"},
		},
	}, s, n)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Fire both alerts.
	alerter.Evaluate(ctx, &MetricSnapshot{
		Host:       &HostMetrics{CPUPercent: 95},
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})

	firing, _ := s.QueryFiringAlerts(ctx)
	if len(firing) != 2 {
		t.Fatalf("pre-shutdown: got %d firing, want 2", len(firing))
	}

	// Simulate shutdown.
	alerter.ResolveAll()

	firing, _ = s.QueryFiringAlerts(ctx)
	if len(firing) != 0 {
		t.Errorf("post-shutdown: got %d firing, want 0", len(firing))
	}
}

// --- 6. Reconnect scenario ---

// TestReconnectSeesExistingAlerts simulates a client disconnecting and
// reconnecting: the new connection should see all firing alerts via snapshot.
func TestReconnectSeesExistingAlerts(t *testing.T) {
	s := testStore(t)
	n := NewNotifier(&NotifyConfig{})
	alerter, err := NewAlerter(map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}, s, n)
	if err != nil {
		t.Fatal(err)
	}

	// Fire an alert.
	alerter.Evaluate(context.Background(), &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})

	_, _, path := testSocketServer(t, s)

	// "First connection" subscribes, gets snapshot, then disconnects.
	conn1 := dial(t, path)
	env := protocol.NewEnvelopeNoBody(protocol.TypeSubscribeAlerts, 1)
	protocol.WriteMsg(conn1, env)
	conn1.SetReadDeadline(time.Now().Add(2 * time.Second))
	msg1, err := protocol.ReadMsg(conn1)
	if err != nil {
		t.Fatal(err)
	}
	var ev1 protocol.AlertEvent
	protocol.DecodeBody(msg1.Body, &ev1)
	if ev1.RuleName != "exited" || ev1.State != "firing" {
		t.Fatalf("first connection: got %q/%q, want exited/firing", ev1.RuleName, ev1.State)
	}
	conn1.Close()

	// "Second connection" (reconnect) — should also see the firing alert.
	conn2, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()

	env = protocol.NewEnvelopeNoBody(protocol.TypeSubscribeAlerts, 2)
	protocol.WriteMsg(conn2, env)
	conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
	msg2, err := protocol.ReadMsg(conn2)
	if err != nil {
		t.Fatal(err)
	}
	var ev2 protocol.AlertEvent
	protocol.DecodeBody(msg2.Body, &ev2)
	if ev2.RuleName != "exited" || ev2.State != "firing" {
		t.Errorf("reconnect: got %q/%q, want exited/firing", ev2.RuleName, ev2.State)
	}
}
