package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
			Project: "myapp", Service: "web",
			CPUPercent: 5.0, MemUsage: 100e6, MemLimit: 512e6, MemPercent: 19.5,
			NetRx: 1000, NetTx: 500, BlockRead: 200, BlockWrite: 100, PIDs: 5,
		},
	}

	if err := s.InsertContainerMetrics(ctx, ts, containers); err != nil {
		t.Fatal(err)
	}

	var project, service string
	var cpu float64
	err := s.db.QueryRow("SELECT project, service, cpu_percent FROM container_metrics WHERE project = ?", "myapp").Scan(&project, &service, &cpu)
	if err != nil {
		t.Fatal(err)
	}
	if project != "myapp" || service != "web" {
		t.Errorf("got project=%q service=%q, want myapp web", project, service)
	}
	if cpu != 5.0 {
		t.Errorf("cpu = %f, want 5.0", cpu)
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
	var resolvedAt *int64
	err = s.db.QueryRow("SELECT rule_name, severity, resolved_at FROM alerts WHERE id = ?", id).
		Scan(&ruleName, &severity, &resolvedAt)
	if err != nil {
		t.Fatal(err)
	}
	if ruleName != "high_cpu" {
		t.Errorf("rule_name = %q, want high_cpu", ruleName)
	}
	if resolvedAt != nil {
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
	if resolvedAt == nil {
		t.Error("resolved_at should be set after resolution")
	}
	if *resolvedAt != resolved.Unix() {
		t.Errorf("resolved_at = %d, want %d", *resolvedAt, resolved.Unix())
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
		{Project: "myapp", Service: "web", CPUPercent: 5},
	})

	results, err := s.QueryContainerMetrics(ctx, ts.Unix(), ts.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Project != "myapp" || results[0].Service != "web" || results[0].CPUPercent != 5 {
		t.Errorf("got project=%q service=%q cpu=%f", results[0].Project, results[0].Service, results[0].CPUPercent)
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

func TestContainerMetricsAllFields(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Now()

	containers := []ContainerMetrics{
		{
			Project: "myapp", Service: "web",
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
	if r.CPUPercent != 5.0 {
		t.Errorf("cpu = %f, want 5.0", r.CPUPercent)
	}
	if r.MemUsage != 100e6 {
		t.Errorf("mem_usage = %d, want %d", r.MemUsage, uint64(100e6))
	}
	if r.NetRx != 1000 {
		t.Errorf("net_rx = %d, want 1000", r.NetRx)
	}
	if r.PIDs != 5 {
		t.Errorf("pids = %d, want 5", r.PIDs)
	}
}

func TestSaveAndLoadTracking(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Save tracking state.
	if err := s.SaveTracking(ctx, []string{"web", "api"}); err != nil {
		t.Fatal(err)
	}

	// Load it back.
	containers, err := s.LoadTracking(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 2 {
		t.Errorf("containers = %d, want 2", len(containers))
	}
}

func TestSaveTrackingOverwrites(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Save initial state.
	s.SaveTracking(ctx, []string{"web", "api"})

	// Overwrite with different state.
	if err := s.SaveTracking(ctx, []string{"db"}); err != nil {
		t.Fatal(err)
	}

	containers, err := s.LoadTracking(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 1 || containers[0] != "db" {
		t.Errorf("containers = %v, want [db]", containers)
	}
}

func TestLoadTrackingEmpty(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	containers, err := s.LoadTracking(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 0 {
		t.Errorf("containers = %v, want empty", containers)
	}
}

func TestQueryContainerMetricsByService(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// Two time points for the same service identity.
	s.InsertContainerMetrics(ctx, ts, []ContainerMetrics{
		{Project: "myapp", Service: "web", CPUPercent: 10.0},
	})
	s.InsertContainerMetrics(ctx, ts.Add(10*time.Second), []ContainerMetrics{
		{Project: "myapp", Service: "web", CPUPercent: 20.0},
	})
	// A different service that should not be returned.
	s.InsertContainerMetrics(ctx, ts, []ContainerMetrics{
		{Project: "myapp", Service: "api", CPUPercent: 30.0},
	})

	results, err := s.QueryContainerMetrics(ctx, ts.Unix(), ts.Add(10*time.Second).Unix(),
		ContainerMetricsFilter{Project: "myapp", Service: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].CPUPercent != 10.0 {
		t.Errorf("results[0].CPUPercent = %f, want 10.0", results[0].CPUPercent)
	}
	if results[1].CPUPercent != 20.0 {
		t.Errorf("results[1].CPUPercent = %f, want 20.0", results[1].CPUPercent)
	}
}

func TestQueryLogsByService(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// Two different container IDs with the same project/service.
	s.InsertLogs(ctx, []LogEntry{
		{Timestamp: ts, ContainerID: "aaa111", ContainerName: "myapp-web-1", Project: "myapp", Service: "web", Stream: "stdout", Message: "from container a"},
		{Timestamp: ts, ContainerID: "bbb222", ContainerName: "myapp-web-2", Project: "myapp", Service: "web", Stream: "stdout", Message: "from container b"},
		// Different service, should not be returned.
		{Timestamp: ts, ContainerID: "ccc333", ContainerName: "myapp-api-1", Project: "myapp", Service: "api", Stream: "stdout", Message: "from api"},
	})

	// Query by service identity.
	results, err := s.QueryLogs(ctx, LogFilter{
		Start:   ts.Unix(),
		End:     ts.Unix(),
		Service: "web",
		Project: "myapp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	// Both containers should be represented.
	ids := map[string]bool{}
	for _, r := range results {
		ids[r.ContainerID] = true
	}
	if !ids["aaa111"] || !ids["bbb222"] {
		t.Errorf("expected aaa111 and bbb222 in results, got %v", ids)
	}

	// Service filter takes precedence over ContainerIDs filter.
	results, err = s.QueryLogs(ctx, LogFilter{
		Start:        ts.Unix(),
		End:          ts.Unix(),
		ContainerIDs: []string{"ccc333"},
		Service:      "web",
		Project:      "myapp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("service precedence: got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.Service != "web" {
			t.Errorf("expected service=web, got %q", r.Service)
		}
	}
}

func TestQueryLogsByServiceNonCompose(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// Non-compose containers: Service set (to container name), but Project is empty.
	s.InsertLogs(ctx, []LogEntry{
		{Timestamp: ts, ContainerID: "xxx111", ContainerName: "myapp", Project: "", Service: "myapp", Stream: "stdout", Message: "line from old container"},
		{Timestamp: ts, ContainerID: "xxx222", ContainerName: "myapp", Project: "", Service: "myapp", Stream: "stdout", Message: "line from new container"},
		// Different non-compose service, should not be returned.
		{Timestamp: ts, ContainerID: "yyy111", ContainerName: "other", Project: "", Service: "other", Stream: "stdout", Message: "unrelated"},
	})

	results, err := s.QueryLogs(ctx, LogFilter{
		Start:   ts.Unix(),
		End:     ts.Unix(),
		Service: "myapp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	ids := map[string]bool{}
	for _, r := range results {
		ids[r.ContainerID] = true
	}
	if !ids["xxx111"] || !ids["xxx222"] {
		t.Errorf("expected xxx111 and xxx222 in results, got %v", ids)
	}
}

func TestCountLogs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.InsertLogs(ctx, []LogEntry{
		{Timestamp: ts, ContainerID: "aaa", ContainerName: "web", Project: "myapp", Service: "web", Stream: "stdout", Message: "hello"},
		{Timestamp: ts, ContainerID: "aaa", ContainerName: "web", Project: "myapp", Service: "web", Stream: "stderr", Message: "error"},
		{Timestamp: ts, ContainerID: "bbb", ContainerName: "api", Project: "myapp", Service: "api", Stream: "stdout", Message: "started"},
		{Timestamp: ts, ContainerID: "ccc", ContainerName: "db", Project: "other", Service: "db", Stream: "stdout", Message: "ready"},
	})

	// Total count (all entries).
	total, err := s.CountLogs(ctx, LogFilter{Start: ts.Unix(), End: ts.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Errorf("total = %d, want 4", total)
	}

	// By container.
	count, err := s.CountLogs(ctx, LogFilter{Start: ts.Unix(), End: ts.Unix(), ContainerIDs: []string{"aaa"}})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("by container = %d, want 2", count)
	}

	// By project.
	count, err = s.CountLogs(ctx, LogFilter{Start: ts.Unix(), End: ts.Unix(), Project: "myapp"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("by project = %d, want 3", count)
	}

	// By service.
	count, err = s.CountLogs(ctx, LogFilter{Start: ts.Unix(), End: ts.Unix(), Service: "web", Project: "myapp"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("by service = %d, want 2", count)
	}
}

func TestQueryLogsRegexSearch(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.InsertLogs(ctx, []LogEntry{
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stdout", Message: "error: connection refused"},
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stdout", Message: "warning: timeout after 30s"},
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stdout", Message: "info: server started on port 8080"},
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stdout", Message: "debug: request id=abc123"},
	})

	tests := []struct {
		name    string
		search  string
		isRegex bool
		want    int
	}{
		{"literal match", "connection refused", false, 1},
		{"regex alternation", "error|warning", true, 2},
		{"regex digit pattern", "\\d+s", true, 1},           // matches "30s"
		{"regex port pattern", "port \\d{4}", true, 1},      // matches "port 8080"
		{"case insensitive", "ERROR", true, 1},               // (?i) prefix applied in QueryLogs
		{"regex anchored", "^info:", true, 1},                // matches line starting with info:
		{"no match", "foobar", false, 0},
		{"dot star", "error.*refused", true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := s.QueryLogs(ctx, LogFilter{
				Start:         ts.Unix(),
				End:           ts.Unix(),
				Search:        tt.search,
				SearchIsRegex: tt.isRegex,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != tt.want {
				t.Errorf("search %q: got %d results, want %d", tt.search, len(results), tt.want)
			}
		})
	}
}

func TestQueryLogsInvalidRegexFallback(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.InsertLogs(ctx, []LogEntry{
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stdout", Message: "open bracket [ in message"},
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stdout", Message: "normal log line"},
	})

	// "[" is invalid regex — should fall back to LIKE match.
	results, err := s.QueryLogs(ctx, LogFilter{
		Start:  ts.Unix(),
		End:    ts.Unix(),
		Search: "[",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("invalid regex fallback: got %d results, want 1", len(results))
	}
}

func TestQueryLogsLevelFilter(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.InsertLogs(ctx, []LogEntry{
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stderr", Message: "fail", Level: "ERR"},
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stderr", Message: "slow", Level: "WARN"},
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stdout", Message: "ok", Level: "INFO"},
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stdout", Message: "trace", Level: "DBUG"},
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stdout", Message: "plain", Level: ""},
	})

	tests := []struct {
		level string
		want  int
	}{
		{"ERR", 1},
		{"WARN", 1},
		{"INFO", 1},
		{"DBUG", 1},
		{"", 5}, // no filter returns all
	}
	for _, tt := range tests {
		t.Run("level_"+tt.level, func(t *testing.T) {
			results, err := s.QueryLogs(ctx, LogFilter{
				Start: ts.Unix(),
				End:   ts.Unix(),
				Level: tt.level,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != tt.want {
				t.Errorf("level %q: got %d results, want %d", tt.level, len(results), tt.want)
			}
		})
	}
}

func TestQueryLogsLevelAndSearch(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.InsertLogs(ctx, []LogEntry{
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stderr", Message: "connection refused", Level: "ERR"},
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stderr", Message: "disk full", Level: "ERR"},
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stderr", Message: "connection timeout", Level: "WARN"},
		{Timestamp: ts, ContainerID: "a", ContainerName: "web", Stream: "stdout", Message: "connection established", Level: "INFO"},
	})

	// Level ERR + search "connection" should match only the first entry.
	results, err := s.QueryLogs(ctx, LogFilter{
		Start:  ts.Unix(),
		End:    ts.Unix(),
		Level:  "ERR",
		Search: "connection",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("level+search: got %d results, want 1", len(results))
	}
	if len(results) > 0 && results[0].Message != "connection refused" {
		t.Errorf("got message %q, want %q", results[0].Message, "connection refused")
	}
}

// --- Chatty container characterization tests ---

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
	const batchSize = 100

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

	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	prePrune, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Prune(ctx, 7); err != nil {
		t.Fatal(err)
	}

	var oldRemaining int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM logs WHERE timestamp = ?", old.Unix()).Scan(&oldRemaining); err != nil {
		t.Fatal(err)
	}
	if oldRemaining != 0 {
		t.Errorf("old entries remaining = %d, want 0", oldRemaining)
	}

	var recentRemaining int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM logs WHERE timestamp = ?", recent.Unix()).Scan(&recentRemaining); err != nil {
		t.Fatal(err)
	}
	if recentRemaining != 100 {
		t.Errorf("recent entries remaining = %d, want 100", recentRemaining)
	}

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

	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	prePrune, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

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

	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	postPrune, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if postPrune.Size() < prePrune.Size() {
		t.Errorf("DB file shrank from %d to %d bytes — unexpected without VACUUM", prePrune.Size(), postPrune.Size())
	}

	t.Logf("DB size: %d bytes pre-prune, %d bytes post-prune (%.1f%% of original)",
		prePrune.Size(), postPrune.Size(), float64(postPrune.Size())/float64(prePrune.Size())*100)
}

func TestResolveOrphanedAlerts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// Insert two unresolved alerts and one resolved.
	s.InsertAlert(ctx, &Alert{
		RuleName: "rule_a", Severity: "critical", Condition: "test",
		InstanceKey: "rule_a:aaa", FiredAt: ts, Message: "firing a",
	})
	id2, _ := s.InsertAlert(ctx, &Alert{
		RuleName: "rule_b", Severity: "warning", Condition: "test",
		InstanceKey: "rule_b:bbb", FiredAt: ts, Message: "firing b",
	})
	s.InsertAlert(ctx, &Alert{
		RuleName: "rule_c", Severity: "warning", Condition: "test",
		InstanceKey: "rule_c:ccc", FiredAt: ts, Message: "already resolved",
	})
	// Resolve rule_c manually.
	s.ResolveAlert(ctx, 3, ts.Add(time.Minute))

	// Resolve orphaned alerts.
	resolveTime := ts.Add(5 * time.Minute)
	if err := s.ResolveOrphanedAlerts(ctx, resolveTime); err != nil {
		t.Fatal(err)
	}

	// All alerts should now be resolved.
	var unresolvedCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE resolved_at IS NULL").Scan(&unresolvedCount); err != nil {
		t.Fatal(err)
	}
	if unresolvedCount != 0 {
		t.Errorf("unresolved alerts = %d, want 0", unresolvedCount)
	}

	// The orphaned alerts should have the new resolve time.
	var resolvedAt int64
	if err := s.db.QueryRow("SELECT resolved_at FROM alerts WHERE id = ?", id2).Scan(&resolvedAt); err != nil {
		t.Fatal(err)
	}
	if resolvedAt != resolveTime.Unix() {
		t.Errorf("resolved_at = %d, want %d", resolvedAt, resolveTime.Unix())
	}
}

func TestQueryFiringAlerts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// Insert one firing and one resolved alert.
	s.InsertAlert(ctx, &Alert{
		RuleName: "firing_rule", Severity: "critical", Condition: "test",
		InstanceKey: "firing_rule:aaa", FiredAt: ts, Message: "still firing",
	})
	id2, _ := s.InsertAlert(ctx, &Alert{
		RuleName: "resolved_rule", Severity: "warning", Condition: "test",
		InstanceKey: "resolved_rule:bbb", FiredAt: ts, Message: "resolved",
	})
	s.ResolveAlert(ctx, id2, ts.Add(time.Minute))

	results, err := s.QueryFiringAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d firing alerts, want 1", len(results))
	}
	if results[0].RuleName != "firing_rule" {
		t.Errorf("rule_name = %q, want firing_rule", results[0].RuleName)
	}
	if results[0].ResolvedAt != nil {
		t.Error("firing alert should have nil ResolvedAt")
	}
}

func TestLogStorageScaling(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Now()
	msg := strings.Repeat("a", 88)

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
