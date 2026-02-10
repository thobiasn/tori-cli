package agent

import (
	"context"
	"database/sql"
	"path/filepath"
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
