package agent

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thobiasn/tori-cli/internal/protocol"
)

// testSocketServer creates a SocketServer on a temp Unix socket.
func testSocketServer(t *testing.T, store *Store) (*SocketServer, *Hub, string) {
	t.Helper()
	hub := NewHub()
	dc := &DockerCollector{
		prevCPU: make(map[string]cpuPrev),
		lastContainers: []Container{
			{ID: "abc123", Name: "web", Image: "nginx", State: "running", Project: "myapp"},
			{ID: "def456", Name: "api", Image: "node", State: "running", Project: "myapp"},
			{ID: "ghi789", Name: "db", Image: "postgres", State: "running", Project: "other"},
		},
		tracked: make(map[string]bool),
	}
	ss := NewSocketServer(hub, store, dc, nil, 7)
	path := filepath.Join(t.TempDir(), "test.sock")
	if err := ss.Start(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Stop() })
	return ss, hub, path
}

// testSocketServerWithAlerter creates a SocketServer with an alerter wired in.
func testSocketServerWithAlerter(t *testing.T, store *Store, alerter *Alerter) (*SocketServer, *Hub, string) {
	t.Helper()
	hub := NewHub()
	dc := &DockerCollector{
		prevCPU: make(map[string]cpuPrev),
		lastContainers: []Container{
			{ID: "abc123", Name: "web", Image: "nginx", State: "running", Project: "myapp"},
		},
		tracked: make(map[string]bool),
	}
	ss := NewSocketServer(hub, store, dc, alerter, 7)
	path := filepath.Join(t.TempDir(), "test.sock")
	if err := ss.Start(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Stop() })
	return ss, hub, path
}

func dial(t *testing.T, path string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestSocketQueryContainers(t *testing.T) {
	s := testStore(t)
	_, _, path := testSocketServer(t, s)
	conn := dial(t, path)

	env := protocol.NewEnvelopeNoBody(protocol.TypeQueryContainers, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != protocol.TypeResult {
		t.Fatalf("type = %q, want result", resp.Type)
	}
	if resp.ID != 1 {
		t.Fatalf("id = %d, want 1", resp.ID)
	}

	var containers protocol.QueryContainersResp
	if err := protocol.DecodeBody(resp.Body, &containers); err != nil {
		t.Fatal(err)
	}
	if len(containers.Containers) != 3 {
		t.Fatalf("containers = %d, want 3", len(containers.Containers))
	}
	if containers.Containers[0].Project != "myapp" {
		t.Errorf("project = %q, want myapp", containers.Containers[0].Project)
	}
}

func TestSocketQueryMetrics(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.InsertHostMetrics(ctx, ts, &HostMetrics{CPUPercent: 42.5})

	_, _, path := testSocketServer(t, s)
	conn := dial(t, path)

	req := protocol.QueryMetricsReq{Start: ts.Unix(), End: ts.Unix()}
	env, err := protocol.NewEnvelope(protocol.TypeQueryMetrics, 2, &req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != 2 {
		t.Fatalf("id = %d, want 2", resp.ID)
	}

	var metrics protocol.QueryMetricsResp
	if err := protocol.DecodeBody(resp.Body, &metrics); err != nil {
		t.Fatal(err)
	}
	if len(metrics.Host) != 1 {
		t.Fatalf("host metrics = %d, want 1", len(metrics.Host))
	}
	if metrics.Host[0].CPUPercent != 42.5 {
		t.Errorf("cpu = %f, want 42.5", metrics.Host[0].CPUPercent)
	}
}

func TestSocketQueryMetricsRangeValidation(t *testing.T) {
	s := testStore(t)
	_, _, path := testSocketServer(t, s)
	conn := dial(t, path)

	// start > end.
	req := protocol.QueryMetricsReq{Start: 2000, End: 1000}
	env, err := protocol.NewEnvelope(protocol.TypeQueryMetrics, 1, &req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != protocol.TypeError {
		t.Fatalf("expected error, got %q", resp.Type)
	}

	// Range too large (7 days retention = 604800s).
	req = protocol.QueryMetricsReq{Start: 0, End: 7*86400 + 1}
	env, err = protocol.NewEnvelope(protocol.TypeQueryMetrics, 2, &req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	resp, err = protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != protocol.TypeError {
		t.Fatalf("expected error for large range, got %q", resp.Type)
	}
}

func TestSocketQueryLogs(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.InsertLogs(ctx, []LogEntry{
		{Timestamp: ts, ContainerID: "abc", ContainerName: "web", Stream: "stdout", Message: "hello"},
	})

	_, _, path := testSocketServer(t, s)
	conn := dial(t, path)

	req := protocol.QueryLogsReq{Start: ts.Unix(), End: ts.Unix()}
	env, err := protocol.NewEnvelope(protocol.TypeQueryLogs, 3, &req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}

	var logs protocol.QueryLogsResp
	if err := protocol.DecodeBody(resp.Body, &logs); err != nil {
		t.Fatal(err)
	}
	if len(logs.Entries) != 1 {
		t.Fatalf("logs = %d, want 1", len(logs.Entries))
	}
	if logs.Entries[0].Message != "hello" {
		t.Errorf("message = %q, want hello", logs.Entries[0].Message)
	}
}

func TestSocketQueryAlerts(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.InsertAlert(ctx, &Alert{
		RuleName: "high_cpu", Severity: "critical", Condition: "host.cpu_percent > 90",
		InstanceKey: "high_cpu", FiredAt: ts, Message: "CPU high",
	})

	_, _, path := testSocketServer(t, s)
	conn := dial(t, path)

	req := protocol.QueryAlertsReq{Start: ts.Unix(), End: ts.Unix()}
	env, err := protocol.NewEnvelope(protocol.TypeQueryAlerts, 4, &req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}

	var alerts protocol.QueryAlertsResp
	if err := protocol.DecodeBody(resp.Body, &alerts); err != nil {
		t.Fatal(err)
	}
	if len(alerts.Alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(alerts.Alerts))
	}
	if alerts.Alerts[0].RuleName != "high_cpu" {
		t.Errorf("rule = %q, want high_cpu", alerts.Alerts[0].RuleName)
	}
}

func TestSocketQueryAlertsResolvedAndAcked(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	id, err := s.InsertAlert(ctx, &Alert{
		RuleName: "high_cpu", Severity: "critical", Condition: "host.cpu_percent > 90",
		InstanceKey: "high_cpu", FiredAt: ts, Message: "CPU high",
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved := ts.Add(30 * time.Second)
	s.ResolveAlert(ctx, id, resolved)
	s.AckAlert(ctx, id)

	_, _, path := testSocketServer(t, s)
	conn := dial(t, path)

	req := protocol.QueryAlertsReq{Start: ts.Unix(), End: ts.Unix()}
	env, err := protocol.NewEnvelope(protocol.TypeQueryAlerts, 1, &req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}

	var alerts protocol.QueryAlertsResp
	if err := protocol.DecodeBody(resp.Body, &alerts); err != nil {
		t.Fatal(err)
	}
	if len(alerts.Alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(alerts.Alerts))
	}
	a := alerts.Alerts[0]
	if a.ResolvedAt != resolved.Unix() {
		t.Errorf("resolved_at = %d, want %d", a.ResolvedAt, resolved.Unix())
	}
	if !a.Acknowledged {
		t.Error("expected acknowledged = true")
	}
}

func TestSocketQueryEmptyResult(t *testing.T) {
	s := testStore(t)
	_, _, path := testSocketServer(t, s)
	conn := dial(t, path)

	// Query with a range that has no data.
	req := protocol.QueryMetricsReq{Start: 1000, End: 2000}
	env, err := protocol.NewEnvelope(protocol.TypeQueryMetrics, 1, &req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != protocol.TypeResult {
		t.Fatalf("type = %q, want result", resp.Type)
	}

	var metrics protocol.QueryMetricsResp
	if err := protocol.DecodeBody(resp.Body, &metrics); err != nil {
		t.Fatal(err)
	}
	// Should succeed with empty slices.
	if len(metrics.Host) != 0 {
		t.Errorf("host = %d, want 0", len(metrics.Host))
	}
}

func TestSocketAckAlert(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.InsertAlert(ctx, &Alert{
		RuleName: "test", Severity: "warning", Condition: "test",
		InstanceKey: "test", FiredAt: time.Now(), Message: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, _, path := testSocketServer(t, s)
	conn := dial(t, path)

	req := protocol.AckAlertReq{AlertID: id}
	env, err := protocol.NewEnvelope(protocol.TypeActionAckAlert, 5, &req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}

	var result protocol.Result
	if err := protocol.DecodeBody(resp.Body, &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("result.OK = false, want true")
	}

	// Verify in DB.
	var ack int
	if err := s.db.QueryRow("SELECT acknowledged FROM alerts WHERE id = ?", id).Scan(&ack); err != nil {
		t.Fatal(err)
	}
	if ack != 1 {
		t.Errorf("acknowledged = %d, want 1", ack)
	}
}

func TestSocketSilenceNoAlerter(t *testing.T) {
	s := testStore(t)
	_, _, path := testSocketServer(t, s) // alerter=nil
	conn := dial(t, path)

	req := protocol.SilenceAlertReq{RuleName: "test", Duration: 60}
	env, err := protocol.NewEnvelope(protocol.TypeActionSilence, 1, &req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != protocol.TypeError {
		t.Fatalf("expected error, got %q", resp.Type)
	}
	var errResult protocol.ErrorResult
	if err := protocol.DecodeBody(resp.Body, &errResult); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errResult.Error, "alerter not configured") {
		t.Errorf("error = %q, want 'alerter not configured'", errResult.Error)
	}
}

func TestSocketSilenceWithAlerter(t *testing.T) {
	s := testStore(t)
	alerter, _ := testAlerter(t, map[string]AlertConfig{
		"high_cpu": {Condition: "host.cpu_percent > 90", Severity: "warning", Actions: []string{"notify"}},
	})

	_, _, path := testSocketServerWithAlerter(t, s, alerter)
	conn := dial(t, path)

	req := protocol.SilenceAlertReq{RuleName: "high_cpu", Duration: 60}
	env, err := protocol.NewEnvelope(protocol.TypeActionSilence, 1, &req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != protocol.TypeResult {
		t.Fatalf("expected result, got %q", resp.Type)
	}
	var result protocol.Result
	if err := protocol.DecodeBody(resp.Body, &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("result.OK = false")
	}
	if !alerter.isSilenced("high_cpu") {
		t.Error("expected high_cpu to be silenced")
	}
}

func TestSocketSilenceValidation(t *testing.T) {
	s := testStore(t)
	alerter, _ := testAlerter(t, map[string]AlertConfig{
		"high_cpu": {Condition: "host.cpu_percent > 90", Severity: "warning", Actions: []string{"notify"}},
	})
	_, _, path := testSocketServerWithAlerter(t, s, alerter)

	tests := []struct {
		name     string
		req      protocol.SilenceAlertReq
		wantErr  string
	}{
		{"negative duration", protocol.SilenceAlertReq{RuleName: "high_cpu", Duration: -1}, "duration"},
		{"too long", protocol.SilenceAlertReq{RuleName: "high_cpu", Duration: maxSilenceDuration + 1}, "duration"},
		{"unknown rule", protocol.SilenceAlertReq{RuleName: "nonexistent", Duration: 60}, "unknown rule"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := dial(t, path)
			env, err := protocol.NewEnvelope(protocol.TypeActionSilence, 1, &tt.req)
			if err != nil {
				t.Fatal(err)
			}
			if err := protocol.WriteMsg(conn, env); err != nil {
				t.Fatal(err)
			}

			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			resp, err := protocol.ReadMsg(conn)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Type != protocol.TypeError {
				t.Fatalf("expected error, got %q", resp.Type)
			}
			var errResult protocol.ErrorResult
			protocol.DecodeBody(resp.Body, &errResult)
			if !strings.Contains(errResult.Error, tt.wantErr) {
				t.Errorf("error = %q, want containing %q", errResult.Error, tt.wantErr)
			}
		})
	}
}

func TestSocketStreamMetrics(t *testing.T) {
	s := testStore(t)
	_, hub, path := testSocketServer(t, s)
	conn := dial(t, path)

	// Subscribe to metrics.
	env := protocol.NewEnvelopeNoBody(protocol.TypeSubscribeMetrics, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	// Publish a metrics update.
	hub.Publish(TopicMetrics, &protocol.MetricsUpdate{
		Timestamp: 1700000000,
		Host:      &protocol.HostMetrics{CPUPercent: 55.5},
	})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	msg, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != protocol.TypeMetricsUpdate {
		t.Fatalf("type = %q, want metrics:update", msg.Type)
	}
	if msg.ID != 0 {
		t.Errorf("streaming message should have ID=0, got %d", msg.ID)
	}

	var update protocol.MetricsUpdate
	if err := protocol.DecodeBody(msg.Body, &update); err != nil {
		t.Fatal(err)
	}
	if update.Host.CPUPercent != 55.5 {
		t.Errorf("cpu = %f, want 55.5", update.Host.CPUPercent)
	}
}

func TestSocketStreamAlerts(t *testing.T) {
	s := testStore(t)
	_, hub, path := testSocketServer(t, s)
	conn := dial(t, path)

	env := protocol.NewEnvelopeNoBody(protocol.TypeSubscribeAlerts, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	hub.Publish(TopicAlerts, &protocol.AlertEvent{
		ID: 1, RuleName: "high_cpu", State: "firing",
	})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	msg, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != protocol.TypeAlertEvent {
		t.Fatalf("type = %q, want alert:event", msg.Type)
	}

	var event protocol.AlertEvent
	if err := protocol.DecodeBody(msg.Body, &event); err != nil {
		t.Fatal(err)
	}
	if event.RuleName != "high_cpu" {
		t.Errorf("rule = %q, want high_cpu", event.RuleName)
	}
}

func TestSocketStreamLogsWithFilters(t *testing.T) {
	s := testStore(t)

	tests := []struct {
		name    string
		filter  protocol.SubscribeLogs
		entries []*protocol.LogEntryMsg
		want    int // how many should pass the filter
	}{
		{
			name:   "container_id filter",
			filter: protocol.SubscribeLogs{ContainerID: "abc123"},
			entries: []*protocol.LogEntryMsg{
				{ContainerID: "abc123", ContainerName: "web", Stream: "stdout", Message: "match"},
				{ContainerID: "def456", ContainerName: "api", Stream: "stdout", Message: "no match"},
			},
			want: 1,
		},
		{
			name:   "stream filter",
			filter: protocol.SubscribeLogs{Stream: "stderr"},
			entries: []*protocol.LogEntryMsg{
				{ContainerID: "abc123", ContainerName: "web", Stream: "stderr", Message: "error"},
				{ContainerID: "abc123", ContainerName: "web", Stream: "stdout", Message: "info"},
			},
			want: 1,
		},
		{
			name:   "search filter",
			filter: protocol.SubscribeLogs{Search: "panic"},
			entries: []*protocol.LogEntryMsg{
				{ContainerID: "abc123", ContainerName: "web", Stream: "stderr", Message: "goroutine panic: oh no"},
				{ContainerID: "abc123", ContainerName: "web", Stream: "stdout", Message: "all good"},
			},
			want: 1,
		},
		{
			name:   "project filter",
			filter: protocol.SubscribeLogs{Project: "myapp"},
			entries: []*protocol.LogEntryMsg{
				{ContainerID: "abc123", ContainerName: "web", Stream: "stdout", Message: "in project"},
				{ContainerID: "ghi789", ContainerName: "db", Stream: "stdout", Message: "other project"},
			},
			want: 1,
		},
		{
			name:   "no filter passes all",
			filter: protocol.SubscribeLogs{},
			entries: []*protocol.LogEntryMsg{
				{ContainerID: "abc123", ContainerName: "web", Stream: "stdout", Message: "one"},
				{ContainerID: "def456", ContainerName: "api", Stream: "stderr", Message: "two"},
			},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, hub, path := testSocketServer(t, s)
			conn := dial(t, path)

			env, err := protocol.NewEnvelope(protocol.TypeSubscribeLogs, 1, &tt.filter)
			if err != nil {
				t.Fatal(err)
			}
			if err := protocol.WriteMsg(conn, env); err != nil {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)

			for _, e := range tt.entries {
				hub.Publish(TopicLogs, e)
			}

			// Read expected messages.
			received := 0
			conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			for {
				msg, err := protocol.ReadMsg(conn)
				if err != nil {
					break
				}
				if msg.Type == protocol.TypeLogEntry {
					received++
				}
			}
			if received != tt.want {
				t.Errorf("received %d log entries, want %d", received, tt.want)
			}
		})
	}
}

func TestSocketUnsubscribe(t *testing.T) {
	s := testStore(t)
	_, hub, path := testSocketServer(t, s)
	conn := dial(t, path)

	// Subscribe.
	env := protocol.NewEnvelopeNoBody(protocol.TypeSubscribeMetrics, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	// Unsubscribe.
	unsub, err := protocol.NewEnvelope(protocol.TypeUnsubscribe, 2, &protocol.Unsubscribe{Topic: TopicMetrics})
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, unsub); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	// Publish — should not receive it since we unsubscribed.
	hub.Publish(TopicMetrics, &protocol.MetricsUpdate{Timestamp: 1})

	// Send a query to verify connection still works.
	q := protocol.NewEnvelopeNoBody(protocol.TypeQueryContainers, 3)
	if err := protocol.WriteMsg(conn, q); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != protocol.TypeResult {
		t.Errorf("got type %q, want result (not metrics:update)", resp.Type)
	}
	if resp.ID != 3 {
		t.Errorf("id = %d, want 3", resp.ID)
	}
}

func TestSocketDuplicateSubscription(t *testing.T) {
	s := testStore(t)
	_, hub, path := testSocketServer(t, s)
	conn := dial(t, path)

	// Subscribe twice.
	env := protocol.NewEnvelopeNoBody(protocol.TypeSubscribeMetrics, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	// Publish one message.
	hub.Publish(TopicMetrics, &protocol.MetricsUpdate{Timestamp: 42})

	// Should receive exactly one.
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	msg, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != protocol.TypeMetricsUpdate {
		t.Fatalf("type = %q, want metrics:update", msg.Type)
	}

	// Second read should timeout (no duplicate).
	_, err = protocol.ReadMsg(conn)
	if err == nil {
		t.Error("expected timeout/error, got a second message (duplicate subscription)")
	}
}

func TestSocketUnknownType(t *testing.T) {
	s := testStore(t)
	_, _, path := testSocketServer(t, s)
	conn := dial(t, path)

	env := protocol.NewEnvelopeNoBody("bogus:type", 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != protocol.TypeError {
		t.Fatalf("type = %q, want error", resp.Type)
	}

	var errResult protocol.ErrorResult
	if err := protocol.DecodeBody(resp.Body, &errResult); err != nil {
		t.Fatal(err)
	}
	if errResult.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestSocketMultipleConnections(t *testing.T) {
	s := testStore(t)
	_, hub, path := testSocketServer(t, s)

	conn1 := dial(t, path)
	conn2 := dial(t, path)

	env := protocol.NewEnvelopeNoBody(protocol.TypeSubscribeMetrics, 1)
	if err := protocol.WriteMsg(conn1, env); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn2, env); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	hub.Publish(TopicMetrics, &protocol.MetricsUpdate{Timestamp: 42})

	for i, conn := range []net.Conn{conn1, conn2} {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		msg, err := protocol.ReadMsg(conn)
		if err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		if msg.Type != protocol.TypeMetricsUpdate {
			t.Errorf("conn %d: type = %q, want metrics:update", i, msg.Type)
		}
	}
}

func TestSocketCleanupOnDisconnect(t *testing.T) {
	s := testStore(t)
	_, hub, path := testSocketServer(t, s)

	// Connect and subscribe.
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}

	env := protocol.NewEnvelopeNoBody(protocol.TypeSubscribeMetrics, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	// Verify subscription works.
	hub.Publish(TopicMetrics, &protocol.MetricsUpdate{Timestamp: 1})
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := protocol.ReadMsg(conn); err != nil {
		t.Fatal(err)
	}

	// Close the connection.
	conn.Close()
	time.Sleep(100 * time.Millisecond)

	// Publishing after disconnect should not panic. Connect a new client
	// and subscribe to verify the hub is clean.
	hub.Publish(TopicMetrics, &protocol.MetricsUpdate{Timestamp: 2})

	conn2 := dial(t, path)
	env2 := protocol.NewEnvelopeNoBody(protocol.TypeSubscribeMetrics, 1)
	if err := protocol.WriteMsg(conn2, env2); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	hub.Publish(TopicMetrics, &protocol.MetricsUpdate{Timestamp: 3})
	conn2.SetReadDeadline(time.Now().Add(time.Second))
	msg, err := protocol.ReadMsg(conn2)
	if err != nil {
		t.Fatal(err)
	}
	var update protocol.MetricsUpdate
	if err := protocol.DecodeBody(msg.Body, &update); err != nil {
		t.Fatal(err)
	}
	if update.Timestamp != 3 {
		t.Errorf("timestamp = %d, want 3", update.Timestamp)
	}
}

func TestSocketConnectionLimit(t *testing.T) {
	s := testStore(t)
	hub := NewHub()
	dc := &DockerCollector{
		prevCPU:           make(map[string]cpuPrev),
		lastContainers:    []Container{},
		tracked: make(map[string]bool),
	}
	// Create a server with a small semaphore for testing.
	ss := &SocketServer{
		hub:     hub,
		store:   s,
		docker:  dc,
		connSem: make(chan struct{}, 2), // max 2 connections
	}
	path := filepath.Join(t.TempDir(), "test.sock")
	if err := ss.Start(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Stop() })

	// Connect 2 (should work).
	conn1 := dial(t, path)
	conn2 := dial(t, path)

	// Verify both work.
	for _, c := range []net.Conn{conn1, conn2} {
		env := protocol.NewEnvelopeNoBody(protocol.TypeQueryContainers, 1)
		if err := protocol.WriteMsg(c, env); err != nil {
			t.Fatal(err)
		}
		c.SetReadDeadline(time.Now().Add(time.Second))
		resp, err := protocol.ReadMsg(c)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Type != protocol.TypeResult {
			t.Errorf("expected result, got %q", resp.Type)
		}
	}

	// 3rd connection should be rejected (closed by server).
	conn3, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn3.Close()

	// Try to communicate — should fail because server closed it.
	env := protocol.NewEnvelopeNoBody(protocol.TypeQueryContainers, 1)
	protocol.WriteMsg(conn3, env)
	conn3.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, err = protocol.ReadMsg(conn3)
	if err == nil {
		t.Error("expected 3rd connection to be rejected")
	}
}

func TestSocketStreamContainers(t *testing.T) {
	s := testStore(t)
	_, hub, path := testSocketServer(t, s)
	conn := dial(t, path)

	// Subscribe to containers.
	env := protocol.NewEnvelopeNoBody(protocol.TypeSubscribeContainers, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	// Publish a container event.
	hub.Publish(TopicContainers, &protocol.ContainerEvent{
		Timestamp:   1700000000,
		ContainerID: "abc123",
		Name:        "web",
		Image:       "nginx",
		State:       "running",
		Action:      "start",
		Project:     "myapp",
	})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	msg, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != protocol.TypeContainerEvent {
		t.Fatalf("type = %q, want container:event", msg.Type)
	}
	if msg.ID != 0 {
		t.Errorf("streaming message should have ID=0, got %d", msg.ID)
	}

	var event protocol.ContainerEvent
	if err := protocol.DecodeBody(msg.Body, &event); err != nil {
		t.Fatal(err)
	}
	if event.ContainerID != "abc123" {
		t.Errorf("container_id = %q, want abc123", event.ContainerID)
	}
	if event.State != "running" {
		t.Errorf("state = %q, want running", event.State)
	}
	if event.Action != "start" {
		t.Errorf("action = %q, want start", event.Action)
	}
	if event.Project != "myapp" {
		t.Errorf("project = %q, want myapp", event.Project)
	}
}

func TestSocketSetTracking(t *testing.T) {
	s := testStore(t)
	ss, _, path := testSocketServer(t, s)
	conn := dial(t, path)

	// Track a container (default is untracked).
	req := protocol.SetTrackingReq{Container: "web", Tracked: true}
	env, err := protocol.NewEnvelope(protocol.TypeActionSetTracking, 1, &req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != protocol.TypeResult {
		t.Fatalf("expected result, got %q", resp.Type)
	}

	// Verify via IsTracked.
	if !ss.docker.IsTracked("web") {
		t.Error("web should be tracked")
	}
	if ss.docker.IsTracked("api") {
		t.Error("api should still be untracked")
	}

	// Verify persisted to DB.
	ctx := t.Context()
	containers, err := s.LoadTracking(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 1 || containers[0] != "web" {
		t.Errorf("persisted containers = %v, want [web]", containers)
	}
}

func TestSocketSetTrackingValidation(t *testing.T) {
	s := testStore(t)
	_, _, path := testSocketServer(t, s)
	conn := dial(t, path)

	// Neither container nor project set.
	req := protocol.SetTrackingReq{Tracked: false}
	env, err := protocol.NewEnvelope(protocol.TypeActionSetTracking, 1, &req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != protocol.TypeError {
		t.Fatalf("expected error, got %q", resp.Type)
	}
}

func TestSocketQueryTracking(t *testing.T) {
	s := testStore(t)
	ss, _, path := testSocketServer(t, s)

	// Track some containers.
	ss.docker.SetTracking("web", "", true)
	ss.docker.SetTracking("api", "", true)

	conn := dial(t, path)
	env := protocol.NewEnvelopeNoBody(protocol.TypeQueryTracking, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != protocol.TypeResult {
		t.Fatalf("expected result, got %q", resp.Type)
	}

	var tracking protocol.QueryTrackingResp
	if err := protocol.DecodeBody(resp.Body, &tracking); err != nil {
		t.Fatal(err)
	}
	if len(tracking.TrackedContainers) != 2 {
		t.Errorf("tracked containers = %v, want 2 entries", tracking.TrackedContainers)
	}
}

func TestSocketQueryContainersTracked(t *testing.T) {
	s := testStore(t)
	ss, _, path := testSocketServer(t, s)

	// Track only "web" (default is untracked).
	ss.docker.SetTracking("web", "", true)

	conn := dial(t, path)
	env := protocol.NewEnvelopeNoBody(protocol.TypeQueryContainers, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}

	var containers protocol.QueryContainersResp
	if err := protocol.DecodeBody(resp.Body, &containers); err != nil {
		t.Fatal(err)
	}

	for _, c := range containers.Containers {
		if c.Name == "web" && !c.Tracked {
			t.Error("web should have Tracked=true")
		}
		if c.Name == "api" && c.Tracked {
			t.Error("api should have Tracked=false")
		}
	}
}

func TestSocketQueryAlertRulesNoAlerter(t *testing.T) {
	s := testStore(t)
	_, _, path := testSocketServer(t, s) // alerter=nil
	conn := dial(t, path)

	env := protocol.NewEnvelopeNoBody(protocol.TypeQueryAlertRules, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != protocol.TypeResult {
		t.Fatalf("expected result, got %q", resp.Type)
	}

	var rules protocol.QueryAlertRulesResp
	if err := protocol.DecodeBody(resp.Body, &rules); err != nil {
		t.Fatal(err)
	}
	if len(rules.Rules) != 0 {
		t.Errorf("rules = %d, want 0 (no alerter)", len(rules.Rules))
	}
}

func TestSocketQueryAlertRulesWithAlerter(t *testing.T) {
	s := testStore(t)
	alerter, _ := testAlerter(t, map[string]AlertConfig{
		"high_cpu": {Condition: "host.cpu_percent > 90", Severity: "critical", Actions: []string{"notify"}},
		"exited":   {Condition: "container.state == 'exited'", Severity: "warning", Actions: []string{"notify"}},
	})
	ctx := t.Context()

	// Fire a container alert.
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	alerter.now = func() time.Time { return now }
	alerter.Evaluate(ctx, &MetricSnapshot{
		Containers: []ContainerMetrics{{ID: "aaa", Name: "web", State: "exited"}},
	})

	_, _, path := testSocketServerWithAlerter(t, s, alerter)
	conn := dial(t, path)

	env := protocol.NewEnvelopeNoBody(protocol.TypeQueryAlertRules, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != protocol.TypeResult {
		t.Fatalf("expected result, got %q", resp.Type)
	}

	var rules protocol.QueryAlertRulesResp
	if err := protocol.DecodeBody(resp.Body, &rules); err != nil {
		t.Fatal(err)
	}
	if len(rules.Rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(rules.Rules))
	}

	// Rules are sorted: exited, high_cpu.
	if rules.Rules[0].Name != "exited" {
		t.Errorf("rules[0].Name = %q, want exited", rules.Rules[0].Name)
	}
	if rules.Rules[0].FiringCount != 1 {
		t.Errorf("exited firing count = %d, want 1", rules.Rules[0].FiringCount)
	}
	if rules.Rules[1].Name != "high_cpu" {
		t.Errorf("rules[1].Name = %q, want high_cpu", rules.Rules[1].Name)
	}
	if rules.Rules[1].FiringCount != 0 {
		t.Errorf("high_cpu firing count = %d, want 0", rules.Rules[1].FiringCount)
	}
}

func TestSocketQueryMetricsDownsampledSkipsDiskNet(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	// Insert 10 host metrics, disk, and net data points.
	for i := 0; i < 10; i++ {
		ts := base.Add(time.Duration(i) * 10 * time.Second)
		s.InsertHostMetrics(ctx, ts, &HostMetrics{CPUPercent: float64(i * 10)})
		s.InsertDiskMetrics(ctx, ts, []DiskMetrics{{Mountpoint: "/", Device: "sda1", Total: 100, Used: 50, Free: 50, Percent: 50}})
		s.InsertNetMetrics(ctx, ts, []NetMetrics{{Iface: "eth0", RxBytes: uint64(i * 1000), TxBytes: uint64(i * 500)}})
	}

	_, _, path := testSocketServer(t, s)
	conn := dial(t, path)

	// Query with Points > 0 (downsampled backfill).
	req := protocol.QueryMetricsReq{Start: base.Unix(), End: base.Add(90 * time.Second).Unix(), Points: 5}
	env, err := protocol.NewEnvelope(protocol.TypeQueryMetrics, 1, &req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != protocol.TypeResult {
		t.Fatalf("type = %q, want result", resp.Type)
	}

	var metrics protocol.QueryMetricsResp
	if err := protocol.DecodeBody(resp.Body, &metrics); err != nil {
		t.Fatal(err)
	}

	// Host should be downsampled to 5 points.
	if len(metrics.Host) != 5 {
		t.Errorf("host = %d, want 5", len(metrics.Host))
	}
	// Disk and net should be empty (skipped for downsampled queries).
	if len(metrics.Disks) != 0 {
		t.Errorf("disks = %d, want 0 (should be skipped when Points > 0)", len(metrics.Disks))
	}
	if len(metrics.Networks) != 0 {
		t.Errorf("networks = %d, want 0 (should be skipped when Points > 0)", len(metrics.Networks))
	}

	// Now query with Points=0 (full resolution) — should include disk/net.
	conn2 := dial(t, path)
	req2 := protocol.QueryMetricsReq{Start: base.Unix(), End: base.Add(90 * time.Second).Unix(), Points: 0}
	env2, err := protocol.NewEnvelope(protocol.TypeQueryMetrics, 2, &req2)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMsg(conn2, env2); err != nil {
		t.Fatal(err)
	}

	conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp2, err := protocol.ReadMsg(conn2)
	if err != nil {
		t.Fatal(err)
	}

	var metrics2 protocol.QueryMetricsResp
	if err := protocol.DecodeBody(resp2.Body, &metrics2); err != nil {
		t.Fatal(err)
	}

	if len(metrics2.Host) != 10 {
		t.Errorf("host (full) = %d, want 10", len(metrics2.Host))
	}
	if len(metrics2.Disks) != 10 {
		t.Errorf("disks (full) = %d, want 10", len(metrics2.Disks))
	}
	if len(metrics2.Networks) != 10 {
		t.Errorf("networks (full) = %d, want 10", len(metrics2.Networks))
	}
}

func TestSocketFileCleanedUpOnStop(t *testing.T) {
	s := testStore(t)
	hub := NewHub()
	dc := &DockerCollector{prevCPU: make(map[string]cpuPrev), tracked: make(map[string]bool)}
	ss := NewSocketServer(hub, s, dc, nil, 7)

	path := filepath.Join(t.TempDir(), "test.sock")
	if err := ss.Start(path); err != nil {
		t.Fatal(err)
	}

	// Verify socket file exists.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket file should exist: %v", err)
	}

	ss.Stop()

	// Verify socket file is removed.
	if _, err := os.Stat(path); err == nil {
		t.Error("socket file should be removed after Stop()")
	}
}
