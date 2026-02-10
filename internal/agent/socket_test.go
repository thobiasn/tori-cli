package agent

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/thobiasn/rook/internal/protocol"
)

// testSocketServer creates a SocketServer on a temp Unix socket.
func testSocketServer(t *testing.T, store *Store) (*SocketServer, *Hub, string) {
	t.Helper()
	hub := NewHub()
	dc := &DockerCollector{
		prevCPU: make(map[string]cpuPrev),
		lastContainers: []Container{
			{ID: "abc123", Name: "web", Image: "nginx", State: "running", Project: "myapp"},
		},
	}
	ss := NewSocketServer(hub, store, dc, nil)
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

	// Send query:containers request.
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
	if len(containers.Containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(containers.Containers))
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

func TestSocketStreamMetrics(t *testing.T) {
	s := testStore(t)
	_, hub, path := testSocketServer(t, s)
	conn := dial(t, path)

	// Subscribe to metrics.
	env := protocol.NewEnvelopeNoBody(protocol.TypeSubscribeMetrics, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}

	// Give the subscription goroutine time to start.
	time.Sleep(50 * time.Millisecond)

	// Publish a metrics update.
	hub.Publish(TopicMetrics, &protocol.MetricsUpdate{
		Timestamp: 1700000000,
		Host:      &protocol.HostMetrics{CPUPercent: 55.5},
	})

	// Read the streamed message.
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

	// Subscribe.
	env := protocol.NewEnvelopeNoBody(protocol.TypeSubscribeAlerts, 1)
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	// Publish an alert event.
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

	// Send a query to verify connection still works and we don't get the metrics update.
	q := protocol.NewEnvelopeNoBody(protocol.TypeQueryContainers, 3)
	if err := protocol.WriteMsg(conn, q); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatal(err)
	}
	// Should be the query response, not a metrics update.
	if resp.Type != protocol.TypeResult {
		t.Errorf("got type %q, want result (not metrics:update)", resp.Type)
	}
	if resp.ID != 3 {
		t.Errorf("id = %d, want 3", resp.ID)
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

	// Both subscribe to metrics.
	env := protocol.NewEnvelopeNoBody(protocol.TypeSubscribeMetrics, 1)
	protocol.WriteMsg(conn1, env)
	protocol.WriteMsg(conn2, env)
	time.Sleep(50 * time.Millisecond)

	// Publish.
	hub.Publish(TopicMetrics, &protocol.MetricsUpdate{Timestamp: 42})

	// Both should receive.
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
