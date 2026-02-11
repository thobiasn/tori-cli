package agent

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenStoreWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var mode string
	err = s.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestInsertAndReadHostMetrics(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Now()

	m := &HostMetrics{
		CPUPercent: 45.5,
		MemTotal:   16 * 1024 * 1024 * 1024,
		MemUsed:    8 * 1024 * 1024 * 1024,
		MemPercent: 50.0,
		SwapTotal:  4 * 1024 * 1024 * 1024,
		SwapUsed:   1 * 1024 * 1024 * 1024,
		Load1:      1.5,
		Load5:      1.2,
		Load15:     0.9,
		Uptime:     86400.5,
	}

	if err := s.InsertHostMetrics(ctx, ts, m); err != nil {
		t.Fatal(err)
	}

	var cpu float64
	var memTotal, memUsed int64
	err := s.db.QueryRow("SELECT cpu_percent, mem_total, mem_used FROM host_metrics WHERE timestamp = ?", ts.Unix()).
		Scan(&cpu, &memTotal, &memUsed)
	if err != nil {
		t.Fatal(err)
	}
	if cpu != 45.5 {
		t.Errorf("cpu = %f, want 45.5", cpu)
	}
	if uint64(memTotal) != m.MemTotal {
		t.Errorf("mem_total = %d, want %d", memTotal, m.MemTotal)
	}
}

func TestInsertDiskMetrics(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Now()

	disks := []DiskMetrics{
		{Mountpoint: "/", Device: "/dev/sda1", Total: 100e9, Used: 50e9, Free: 50e9, Percent: 50.0},
		{Mountpoint: "/home", Device: "/dev/sda2", Total: 200e9, Used: 100e9, Free: 100e9, Percent: 50.0},
	}

	if err := s.InsertDiskMetrics(ctx, ts, disks); err != nil {
		t.Fatal(err)
	}

	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM disk_metrics WHERE timestamp = ?", ts.Unix()).Scan(&count)
	if count != 2 {
		t.Errorf("disk rows = %d, want 2", count)
	}
}

func TestInsertNetMetrics(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Now()

	nets := []NetMetrics{
		{Iface: "eth0", RxBytes: 1000, TxBytes: 500, RxPackets: 10, TxPackets: 5, RxErrors: 0, TxErrors: 0},
	}

	if err := s.InsertNetMetrics(ctx, ts, nets); err != nil {
		t.Fatal(err)
	}

	var iface string
	var rxBytes int64
	err := s.db.QueryRow("SELECT iface, rx_bytes FROM net_metrics WHERE timestamp = ?", ts.Unix()).Scan(&iface, &rxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if iface != "eth0" {
		t.Errorf("iface = %q, want eth0", iface)
	}
}

func TestInsertContainerMetrics(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Now()

	containers := []ContainerMetrics{
		{
			ID: "abc123", Name: "web", Image: "nginx:latest", State: "running",
			CPUPercent: 5.0, MemUsage: 100e6, MemLimit: 512e6, MemPercent: 19.5,
			NetRx: 1000, NetTx: 500, BlockRead: 200, BlockWrite: 100, PIDs: 5,
		},
	}

	if err := s.InsertContainerMetrics(ctx, ts, containers); err != nil {
		t.Fatal(err)
	}

	var name, state string
	err := s.db.QueryRow("SELECT name, state FROM container_metrics WHERE id = ?", "abc123").Scan(&name, &state)
	if err != nil {
		t.Fatal(err)
	}
	if name != "web" || state != "running" {
		t.Errorf("got name=%q state=%q, want web running", name, state)
	}
}

func TestInsertLogs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	entries := []LogEntry{
		{Timestamp: time.Now(), ContainerID: "abc123", ContainerName: "web", Stream: "stdout", Message: "hello world"},
		{Timestamp: time.Now(), ContainerID: "abc123", ContainerName: "web", Stream: "stderr", Message: "error occurred"},
	}

	if err := s.InsertLogs(ctx, entries); err != nil {
		t.Fatal(err)
	}

	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM logs WHERE container_id = ?", "abc123").Scan(&count)
	if count != 2 {
		t.Errorf("log rows = %d, want 2", count)
	}
}

func TestInsertLogsEmpty(t *testing.T) {
	s := testStore(t)
	if err := s.InsertLogs(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestPrune(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	old := time.Now().Add(-10 * 24 * time.Hour)
	recent := time.Now()

	// Insert old and recent host metrics
	s.InsertHostMetrics(ctx, old, &HostMetrics{CPUPercent: 10})
	s.InsertHostMetrics(ctx, recent, &HostMetrics{CPUPercent: 20})

	// Insert old and recent logs
	s.InsertLogs(ctx, []LogEntry{
		{Timestamp: old, ContainerID: "old", ContainerName: "old", Stream: "stdout", Message: "old"},
	})
	s.InsertLogs(ctx, []LogEntry{
		{Timestamp: recent, ContainerID: "new", ContainerName: "new", Stream: "stdout", Message: "new"},
	})

	if err := s.Prune(ctx, 7); err != nil {
		t.Fatal(err)
	}

	// Old host metric should be pruned
	var hostCount int
	s.db.QueryRow("SELECT COUNT(*) FROM host_metrics").Scan(&hostCount)
	if hostCount != 1 {
		t.Errorf("host_metrics after prune = %d, want 1", hostCount)
	}

	// Check the remaining is the recent one
	var cpu float64
	s.db.QueryRow("SELECT cpu_percent FROM host_metrics").Scan(&cpu)
	if cpu != 20 {
		t.Errorf("remaining cpu = %f, want 20", cpu)
	}

	// Old log should be pruned
	var logCount int
	s.db.QueryRow("SELECT COUNT(*) FROM logs").Scan(&logCount)
	if logCount != 1 {
		t.Errorf("logs after prune = %d, want 1", logCount)
	}
}

func TestInsertAndResolveAlert(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	fired := time.Now()
	a := &Alert{
		RuleName:    "high_cpu",
		Severity:    "critical",
		Condition:   "host.cpu_percent > 90",
		InstanceKey: "high_cpu",
		FiredAt:     fired,
		Message:     "CPU high",
	}

	id, err := s.InsertAlert(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected non-zero ID")
	}

	// Verify it's in the DB.
	var ruleName, severity string
	var resolvedAt sql.NullInt64
	err = s.db.QueryRow("SELECT rule_name, severity, resolved_at FROM alerts WHERE id = ?", id).
		Scan(&ruleName, &severity, &resolvedAt)
	if err != nil {
		t.Fatal(err)
	}
	if ruleName != "high_cpu" {
		t.Errorf("rule_name = %q, want high_cpu", ruleName)
	}
	if severity != "critical" {
		t.Errorf("severity = %q, want critical", severity)
	}
	if resolvedAt.Valid {
		t.Error("resolved_at should be NULL before resolution")
	}

	// Resolve.
	resolved := fired.Add(30 * time.Second)
	if err := s.ResolveAlert(ctx, id, resolved); err != nil {
		t.Fatal(err)
	}

	err = s.db.QueryRow("SELECT resolved_at FROM alerts WHERE id = ?", id).Scan(&resolvedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !resolvedAt.Valid {
		t.Error("resolved_at should be set after resolution")
	}
	if resolvedAt.Int64 != resolved.Unix() {
		t.Errorf("resolved_at = %d, want %d", resolvedAt.Int64, resolved.Unix())
	}
}

func TestPruneAlerts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	old := time.Now().Add(-10 * 24 * time.Hour)
	recent := time.Now()

	s.InsertAlert(ctx, &Alert{
		RuleName: "old", Severity: "warning", Condition: "test",
		InstanceKey: "old", FiredAt: old, Message: "old alert",
	})
	s.InsertAlert(ctx, &Alert{
		RuleName: "new", Severity: "warning", Condition: "test",
		InstanceKey: "new", FiredAt: recent, Message: "new alert",
	})

	if err := s.Prune(ctx, 7); err != nil {
		t.Fatal(err)
	}

	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&count)
	if count != 1 {
		t.Errorf("alerts after prune = %d, want 1", count)
	}

	var ruleName string
	s.db.QueryRow("SELECT rule_name FROM alerts").Scan(&ruleName)
	if ruleName != "new" {
		t.Errorf("remaining alert = %q, want new", ruleName)
	}
}

func TestQueryHostMetrics(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(10 * time.Second)
	t3 := t1.Add(20 * time.Second)

	s.InsertHostMetrics(ctx, t1, &HostMetrics{CPUPercent: 10})
	s.InsertHostMetrics(ctx, t2, &HostMetrics{CPUPercent: 20})
	s.InsertHostMetrics(ctx, t3, &HostMetrics{CPUPercent: 30})

	results, err := s.QueryHostMetrics(ctx, t1.Unix(), t2.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].CPUPercent != 10 || results[1].CPUPercent != 20 {
		t.Errorf("cpu values = %f, %f; want 10, 20", results[0].CPUPercent, results[1].CPUPercent)
	}
	if results[0].Timestamp.Unix() != t1.Unix() {
		t.Errorf("timestamp = %v, want %v", results[0].Timestamp, t1)
	}
}

func TestQueryDiskMetrics(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.InsertDiskMetrics(ctx, ts, []DiskMetrics{
		{Mountpoint: "/", Device: "/dev/sda1", Total: 100e9, Used: 50e9, Free: 50e9, Percent: 50},
		{Mountpoint: "/home", Device: "/dev/sda2", Total: 200e9, Used: 100e9, Free: 100e9, Percent: 50},
	})

	results, err := s.QueryDiskMetrics(ctx, ts.Unix(), ts.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Mountpoint != "/" || results[1].Mountpoint != "/home" {
		t.Errorf("mountpoints = %q, %q", results[0].Mountpoint, results[1].Mountpoint)
	}
}

func TestQueryNetMetrics(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.InsertNetMetrics(ctx, ts, []NetMetrics{
		{Iface: "eth0", RxBytes: 1000, TxBytes: 500},
	})

	results, err := s.QueryNetMetrics(ctx, ts.Unix(), ts.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Iface != "eth0" || results[0].RxBytes != 1000 {
		t.Errorf("got iface=%q rx=%d", results[0].Iface, results[0].RxBytes)
	}
}

func TestQueryContainerMetrics(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.InsertContainerMetrics(ctx, ts, []ContainerMetrics{
		{ID: "abc", Name: "web", Image: "nginx", State: "running", CPUPercent: 5},
	})

	results, err := s.QueryContainerMetrics(ctx, ts.Unix(), ts.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].ID != "abc" || results[0].CPUPercent != 5 {
		t.Errorf("got id=%q cpu=%f", results[0].ID, results[0].CPUPercent)
	}
}

func TestQueryLogs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.InsertLogs(ctx, []LogEntry{
		{Timestamp: ts, ContainerID: "aaa", ContainerName: "web", Stream: "stdout", Message: "hello world"},
		{Timestamp: ts, ContainerID: "aaa", ContainerName: "web", Stream: "stderr", Message: "error occurred"},
		{Timestamp: ts, ContainerID: "bbb", ContainerName: "api", Stream: "stdout", Message: "started"},
	})

	// Query all.
	results, err := s.QueryLogs(ctx, LogFilter{Start: ts.Unix(), End: ts.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	// Filter by container.
	results, err = s.QueryLogs(ctx, LogFilter{Start: ts.Unix(), End: ts.Unix(), ContainerIDs: []string{"aaa"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("container filter: got %d, want 2", len(results))
	}

	// Filter by stream.
	results, err = s.QueryLogs(ctx, LogFilter{Start: ts.Unix(), End: ts.Unix(), Stream: "stderr"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("stream filter: got %d, want 1", len(results))
	}

	// Filter by search.
	results, err = s.QueryLogs(ctx, LogFilter{Start: ts.Unix(), End: ts.Unix(), Search: "error"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("search filter: got %d, want 1", len(results))
	}

	// Limit.
	results, err = s.QueryLogs(ctx, LogFilter{Start: ts.Unix(), End: ts.Unix(), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("limit: got %d, want 1", len(results))
	}
}

func TestQueryLogsMultipleContainers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.InsertLogs(ctx, []LogEntry{
		{Timestamp: ts, ContainerID: "aaa", ContainerName: "web", Stream: "stdout", Message: "a"},
		{Timestamp: ts, ContainerID: "bbb", ContainerName: "api", Stream: "stdout", Message: "b"},
		{Timestamp: ts, ContainerID: "ccc", ContainerName: "db", Stream: "stdout", Message: "c"},
	})

	results, err := s.QueryLogs(ctx, LogFilter{
		Start:        ts.Unix(),
		End:          ts.Unix(),
		ContainerIDs: []string{"aaa", "bbb"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
}

func TestQueryAlerts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)

	s.InsertAlert(ctx, &Alert{
		RuleName: "high_cpu", Severity: "critical", Condition: "host.cpu_percent > 90",
		InstanceKey: "high_cpu", FiredAt: t1, Message: "CPU high",
	})
	s.InsertAlert(ctx, &Alert{
		RuleName: "disk_full", Severity: "warning", Condition: "host.disk_percent > 90",
		InstanceKey: "disk_full:/", FiredAt: t2, Message: "Disk full",
	})

	results, err := s.QueryAlerts(ctx, t1.Unix(), t2.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	// Ordered by fired_at DESC.
	if results[0].RuleName != "disk_full" {
		t.Errorf("first result = %q, want disk_full", results[0].RuleName)
	}
}

func TestAckAlert(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	id, err := s.InsertAlert(ctx, &Alert{
		RuleName: "high_cpu", Severity: "critical", Condition: "test",
		InstanceKey: "high_cpu", FiredAt: time.Now(), Message: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.AckAlert(ctx, id); err != nil {
		t.Fatal(err)
	}

	var ack int
	if err := s.db.QueryRow("SELECT acknowledged FROM alerts WHERE id = ?", id).Scan(&ack); err != nil {
		t.Fatal(err)
	}
	if ack != 1 {
		t.Errorf("acknowledged = %d, want 1", ack)
	}
}

func TestQueryAlertsResolvedAndAcknowledged(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	id, err := s.InsertAlert(ctx, &Alert{
		RuleName: "high_cpu", Severity: "critical", Condition: "host.cpu_percent > 90",
		InstanceKey: "high_cpu", FiredAt: ts, Message: "CPU high",
	})
	if err != nil {
		t.Fatal(err)
	}

	resolved := ts.Add(30 * time.Second)
	if err := s.ResolveAlert(ctx, id, resolved); err != nil {
		t.Fatal(err)
	}
	if err := s.AckAlert(ctx, id); err != nil {
		t.Fatal(err)
	}

	results, err := s.QueryAlerts(ctx, ts.Unix(), ts.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	a := results[0]
	if a.ResolvedAt == nil {
		t.Fatal("ResolvedAt should be set")
	}
	if a.ResolvedAt.Unix() != resolved.Unix() {
		t.Errorf("ResolvedAt = %v, want %v", a.ResolvedAt, resolved)
	}
	if !a.Acknowledged {
		t.Error("expected acknowledged = true")
	}
}

func TestAckAlertNotFound(t *testing.T) {
	s := testStore(t)
	err := s.AckAlert(context.Background(), 9999)
	if err == nil {
		t.Fatal("expected error for non-existent alert")
	}
}

func TestSchemaCreation(t *testing.T) {
	s := testStore(t)

	// Verify all tables exist
	tables := []string{"host_metrics", "disk_metrics", "net_metrics", "container_metrics", "logs", "alerts"}
	for _, table := range tables {
		var name string
		err := s.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("table %q not created", table)
		} else if err != nil {
			t.Errorf("checking table %q: %v", table, err)
		}
	}
}

func TestHostMetricsNewFields(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Now()

	m := &HostMetrics{
		CPUPercent: 45.5,
		MemTotal:   16e9,
		MemUsed:    8e9,
		MemPercent: 50.0,
		MemCached:  2e9,
		MemFree:    6e9,
	}
	if err := s.InsertHostMetrics(ctx, ts, m); err != nil {
		t.Fatal(err)
	}

	results, err := s.QueryHostMetrics(ctx, ts.Unix(), ts.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].MemCached != 2e9 {
		t.Errorf("mem_cached = %d, want %d", results[0].MemCached, uint64(2e9))
	}
	if results[0].MemFree != 6e9 {
		t.Errorf("mem_free = %d, want %d", results[0].MemFree, uint64(6e9))
	}
}

func TestContainerMetricsNewFields(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Now()

	containers := []ContainerMetrics{
		{
			ID: "abc123", Name: "web", Image: "nginx:latest", State: "running",
			Health: "healthy", StartedAt: 1700000000, RestartCount: 3, ExitCode: 0,
			CPUPercent: 5.0, MemUsage: 100e6, MemLimit: 512e6, MemPercent: 19.5,
			NetRx: 1000, NetTx: 500, BlockRead: 200, BlockWrite: 100, PIDs: 5,
		},
	}

	if err := s.InsertContainerMetrics(ctx, ts, containers); err != nil {
		t.Fatal(err)
	}

	results, err := s.QueryContainerMetrics(ctx, ts.Unix(), ts.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.Health != "healthy" {
		t.Errorf("health = %q, want healthy", r.Health)
	}
	if r.StartedAt != 1700000000 {
		t.Errorf("started_at = %d, want 1700000000", r.StartedAt)
	}
	if r.RestartCount != 3 {
		t.Errorf("restart_count = %d, want 3", r.RestartCount)
	}
	if r.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", r.ExitCode)
	}
}

func TestEnsureColumnsIdempotent(t *testing.T) {
	s := testStore(t)
	// Call ensureColumns again — should not error.
	s.ensureColumns()

	// Verify columns still work.
	ctx := context.Background()
	ts := time.Now()
	m := &HostMetrics{MemCached: 1024, MemFree: 2048}
	if err := s.InsertHostMetrics(ctx, ts, m); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureColumnsOldSchema(t *testing.T) {
	// Create a database with the old schema (missing added columns),
	// then verify ensureColumns adds them.
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.Exec("PRAGMA journal_mode=WAL")

	// Old schema: host_metrics without mem_cached/mem_free,
	// container_metrics without health/started_at/restart_count/exit_code.
	oldSchema := `
CREATE TABLE IF NOT EXISTS host_metrics (
	timestamp    INTEGER NOT NULL,
	cpu_percent  REAL    NOT NULL,
	mem_total    INTEGER NOT NULL,
	mem_used     INTEGER NOT NULL,
	mem_percent  REAL    NOT NULL,
	swap_total   INTEGER NOT NULL,
	swap_used    INTEGER NOT NULL,
	load1        REAL    NOT NULL,
	load5        REAL    NOT NULL,
	load15       REAL    NOT NULL,
	uptime       REAL    NOT NULL
);
CREATE TABLE IF NOT EXISTS container_metrics (
	timestamp  INTEGER NOT NULL,
	id         TEXT    NOT NULL,
	name       TEXT    NOT NULL,
	image      TEXT    NOT NULL,
	state      TEXT    NOT NULL,
	cpu_percent REAL   NOT NULL,
	mem_usage  INTEGER NOT NULL,
	mem_limit  INTEGER NOT NULL,
	mem_percent REAL   NOT NULL,
	net_rx     INTEGER NOT NULL,
	net_tx     INTEGER NOT NULL,
	block_read INTEGER NOT NULL,
	block_write INTEGER NOT NULL,
	pids       INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS disk_metrics (timestamp INTEGER, mountpoint TEXT, device TEXT, total INTEGER, used INTEGER, free INTEGER, percent REAL);
CREATE TABLE IF NOT EXISTS net_metrics (timestamp INTEGER, iface TEXT, rx_bytes INTEGER, tx_bytes INTEGER, rx_packets INTEGER, tx_packets INTEGER, rx_errors INTEGER, tx_errors INTEGER);
CREATE TABLE IF NOT EXISTS logs (timestamp INTEGER, container_id TEXT, container_name TEXT, stream TEXT, message TEXT);
CREATE TABLE IF NOT EXISTS alerts (id INTEGER PRIMARY KEY AUTOINCREMENT, rule_name TEXT, severity TEXT, condition TEXT, instance_key TEXT, fired_at INTEGER, resolved_at INTEGER, message TEXT, acknowledged INTEGER DEFAULT 0);
`
	if _, err := db.Exec(oldSchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	// Now open via OpenStore which runs ensureColumns.
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Verify the new columns exist by inserting data that uses them.
	ctx := context.Background()
	ts := time.Now()
	if err := s.InsertHostMetrics(ctx, ts, &HostMetrics{MemCached: 100, MemFree: 200}); err != nil {
		t.Fatalf("inserting with new host columns: %v", err)
	}
	if err := s.InsertContainerMetrics(ctx, ts, []ContainerMetrics{
		{ID: "x", Name: "x", Image: "x", State: "running", Health: "healthy", StartedAt: 1000, RestartCount: 2, ExitCode: 1},
	}); err != nil {
		t.Fatalf("inserting with new container columns: %v", err)
	}

	// Verify values round-trip.
	results, err := s.QueryContainerMetrics(ctx, ts.Unix(), ts.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Health != "healthy" {
		t.Errorf("health = %q, want healthy", results[0].Health)
	}
	if results[0].RestartCount != 2 {
		t.Errorf("restart_count = %d, want 2", results[0].RestartCount)
	}
}

// --- Chatty container characterization tests ---
// These tests document the current unbounded log ingestion behavior.
// There is no throughput cap, no per-container size budget, and DELETE
// does not reclaim disk space without VACUUM.

func TestHighVolumeLogInsertion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	ts := time.Now()

	const totalEntries = 10_000
	const batchSize = 100 // matches logBatchSize

	for i := 0; i < totalEntries/batchSize; i++ {
		batch := make([]LogEntry, batchSize)
		for j := range batch {
			batch[j] = LogEntry{
				Timestamp:     ts,
				ContainerID:   "chatty",
				ContainerName: "chatty",
				Stream:        "stdout",
				Message:       fmt.Sprintf("log line %05d: %s", i*batchSize+j, strings.Repeat("x", 80)),
			}
		}
		if err := s.InsertLogs(ctx, batch); err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
	}

	// Characterization: all 10,000 entries are stored — no cap exists.
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM logs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != totalEntries {
		t.Errorf("stored %d entries, want %d — expected no cap on log insertion", count, totalEntries)
	}

	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("DB size after %d log entries (~100 bytes each): %.2f MB", totalEntries, float64(info.Size())/(1024*1024))
}

func TestPruneHighVolumeLogs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	old := time.Now().Add(-8 * 24 * time.Hour)
	recent := time.Now()

	// Insert 10,000 old entries (8 days ago — outside 7-day retention).
	const oldCount = 10_000
	for i := 0; i < oldCount/100; i++ {
		batch := make([]LogEntry, 100)
		for j := range batch {
			batch[j] = LogEntry{
				Timestamp:     old,
				ContainerID:   "chatty",
				ContainerName: "chatty",
				Stream:        "stdout",
				Message:       fmt.Sprintf("old line %d", i*100+j),
			}
		}
		if err := s.InsertLogs(ctx, batch); err != nil {
			t.Fatalf("old batch %d: %v", i, err)
		}
	}

	// Insert 100 recent entries.
	recentBatch := make([]LogEntry, 100)
	for i := range recentBatch {
		recentBatch[i] = LogEntry{
			Timestamp:     recent,
			ContainerID:   "chatty",
			ContainerName: "chatty",
			Stream:        "stdout",
			Message:       fmt.Sprintf("recent line %d", i),
		}
	}
	if err := s.InsertLogs(ctx, recentBatch); err != nil {
		t.Fatal(err)
	}

	// Checkpoint WAL for accurate file size measurement.
	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	prePrune, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Prune(ctx, 7); err != nil {
		t.Fatal(err)
	}

	// Old entries should be gone.
	var oldRemaining int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM logs WHERE timestamp = ?", old.Unix()).Scan(&oldRemaining); err != nil {
		t.Fatal(err)
	}
	if oldRemaining != 0 {
		t.Errorf("old entries remaining = %d, want 0", oldRemaining)
	}

	// Recent entries should survive.
	var recentRemaining int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM logs WHERE timestamp = ?", recent.Unix()).Scan(&recentRemaining); err != nil {
		t.Fatal(err)
	}
	if recentRemaining != 100 {
		t.Errorf("recent entries remaining = %d, want 100", recentRemaining)
	}

	// Checkpoint and measure post-prune size.
	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	postPrune, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("DB size before prune: %.2f MB, after prune: %.2f MB (DELETE does not reclaim space without VACUUM)",
		float64(prePrune.Size())/(1024*1024), float64(postPrune.Size())/(1024*1024))
}

func TestPruneDoesNotShrinkDBFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	old := time.Now().Add(-2 * 24 * time.Hour)

	// Insert 5,000 entries with ~100 byte messages.
	const totalEntries = 5_000
	for i := 0; i < totalEntries/100; i++ {
		batch := make([]LogEntry, 100)
		for j := range batch {
			batch[j] = LogEntry{
				Timestamp:     old,
				ContainerID:   "chatty",
				ContainerName: "chatty",
				Stream:        "stdout",
				Message:       fmt.Sprintf("line %d: %s", i*100+j, strings.Repeat("x", 80)),
			}
		}
		if err := s.InsertLogs(ctx, batch); err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
	}

	// Checkpoint WAL so main file reflects all data.
	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	prePrune, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// Prune all entries (retention 1 day, entries are 2 days old).
	if err := s.Prune(ctx, 1); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM logs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("logs after prune = %d, want 0", count)
	}

	// Checkpoint again for accurate post-prune measurement.
	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	postPrune, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// File should NOT shrink — SQLite DELETE does not reclaim disk space.
	if postPrune.Size() < prePrune.Size() {
		t.Errorf("DB file shrank from %d to %d bytes — unexpected without VACUUM", prePrune.Size(), postPrune.Size())
	}

	t.Logf("DB size: %d bytes pre-prune, %d bytes post-prune (%.1f%% of original)",
		prePrune.Size(), postPrune.Size(), float64(postPrune.Size())/float64(prePrune.Size())*100)
}

func TestLogStorageScaling(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Now()
	msg := strings.Repeat("a", 88) // ~100 bytes per entry with overhead

	targets := []int{100, 1000, 5000}
	inserted := 0

	for _, target := range targets {
		for inserted < target {
			n := target - inserted
			if n > 100 {
				n = 100
			}
			batch := make([]LogEntry, n)
			for j := range batch {
				batch[j] = LogEntry{
					Timestamp:     ts,
					ContainerID:   "chatty",
					ContainerName: "chatty",
					Stream:        "stdout",
					Message:       msg,
				}
			}
			if err := s.InsertLogs(ctx, batch); err != nil {
				t.Fatal(err)
			}
			inserted += n
		}

		var pageCount, pageSize int64
		if err := s.db.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
			t.Fatal(err)
		}
		dbSize := pageCount * pageSize
		bytesPerEntry := float64(dbSize) / float64(target)
		t.Logf("%5d entries: DB = %d bytes (%.2f MB), %.0f bytes/entry",
			target, dbSize, float64(dbSize)/(1024*1024), bytesPerEntry)
	}
}
