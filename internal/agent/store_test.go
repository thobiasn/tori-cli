package agent

import (
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

func TestSchemaCreation(t *testing.T) {
	s := testStore(t)

	// Verify all tables exist
	tables := []string{"host_metrics", "disk_metrics", "net_metrics", "container_metrics", "logs", "alerts", "tracking_state"}
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

func TestEnsureColumnsIdempotent(t *testing.T) {
	s := testStore(t)
	// Call ensureColumns again — should not error.
	s.ensureColumns()

	// Verify columns still work.
	ctx := t.Context()
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
	ctx := t.Context()
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
