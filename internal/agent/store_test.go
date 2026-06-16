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

func TestBackfillLogFields(t *testing.T) {
	// Create a v1 database (container_metrics migration done, log backfill not yet).
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.Exec("PRAGMA journal_mode=WAL")

	// v1 schema: logs without level/display_msg columns.
	v1Schema := `
CREATE TABLE IF NOT EXISTS host_metrics (
	timestamp INTEGER, cpu_percent REAL, mem_total INTEGER, mem_used INTEGER,
	mem_percent REAL, swap_total INTEGER, swap_used INTEGER,
	load1 REAL, load5 REAL, load15 REAL, uptime REAL
);
CREATE TABLE IF NOT EXISTS container_metrics (
	timestamp INTEGER, project TEXT, service TEXT, cpu_percent REAL,
	mem_usage INTEGER, mem_limit INTEGER, mem_percent REAL,
	net_rx INTEGER, net_tx INTEGER, block_read INTEGER, block_write INTEGER, pids INTEGER
);
CREATE TABLE IF NOT EXISTS disk_metrics (timestamp INTEGER, mountpoint TEXT, device TEXT, total INTEGER, used INTEGER, free INTEGER, percent REAL);
CREATE TABLE IF NOT EXISTS net_metrics (timestamp INTEGER, iface TEXT, rx_bytes INTEGER, tx_bytes INTEGER, rx_packets INTEGER, tx_packets INTEGER, rx_errors INTEGER, tx_errors INTEGER);
CREATE TABLE IF NOT EXISTS logs (timestamp INTEGER, container_id TEXT, container_name TEXT, stream TEXT, message TEXT);
CREATE TABLE IF NOT EXISTS alerts (id INTEGER PRIMARY KEY AUTOINCREMENT, rule_name TEXT, severity TEXT, condition TEXT, instance_key TEXT, fired_at INTEGER, resolved_at INTEGER, message TEXT, acknowledged INTEGER DEFAULT 0);
CREATE TABLE IF NOT EXISTS tracking_state (kind TEXT NOT NULL, name TEXT NOT NULL, UNIQUE(kind, name));
PRAGMA user_version = 1;
`
	if _, err := db.Exec(v1Schema); err != nil {
		db.Close()
		t.Fatal(err)
	}

	ts := time.Now().Unix()
	// JSON message — should get level=INFO, display_msg="started".
	db.Exec(`INSERT INTO logs (timestamp, container_id, container_name, stream, message) VALUES (?, 'a', 'web', 'stdout', ?)`,
		ts, `{"level":"info","msg":"started"}`)
	// JSON error — should get level=ERR, display_msg="connection refused".
	db.Exec(`INSERT INTO logs (timestamp, container_id, container_name, stream, message) VALUES (?, 'a', 'web', 'stderr', ?)`,
		ts, `{"level":"error","message":"connection refused"}`)
	// Plain text — should get level="", display_msg=message itself.
	db.Exec(`INSERT INTO logs (timestamp, container_id, container_name, stream, message) VALUES (?, 'a', 'web', 'stdout', ?)`,
		ts, "just a plain log message")
	db.Close()

	// OpenStore triggers v1→v2 migration.
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Verify backfilled values.
	rows, err := s.db.Query("SELECT message, level, display_msg FROM logs ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	type row struct {
		message, level, displayMsg string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.message, &r.level, &r.displayMsg); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}

	// JSON info.
	if got[0].level != "INFO" {
		t.Errorf("row 0 level = %q, want INFO", got[0].level)
	}
	if got[0].displayMsg != "started" {
		t.Errorf("row 0 display_msg = %q, want 'started'", got[0].displayMsg)
	}

	// JSON error.
	if got[1].level != "ERR" {
		t.Errorf("row 1 level = %q, want ERR", got[1].level)
	}
	if got[1].displayMsg != "connection refused" {
		t.Errorf("row 1 display_msg = %q, want 'connection refused'", got[1].displayMsg)
	}

	// Plain text.
	if got[2].level != "" {
		t.Errorf("row 2 level = %q, want empty", got[2].level)
	}
	if got[2].displayMsg != "just a plain log message" {
		t.Errorf("row 2 display_msg = %q, want original message", got[2].displayMsg)
	}

	// user_version should be 2.
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Errorf("user_version = %d, want %d", version, currentSchemaVersion)
	}
}

func TestBackfillLogFieldsEmptyMessages(t *testing.T) {
	// Verify that rows with empty messages don't cause an infinite loop.
	// After parsing, both level and display_msg are "" for these rows,
	// so the WHERE clause would re-match them without the batchSize break.
	path := filepath.Join(t.TempDir(), "v1empty.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.Exec("PRAGMA journal_mode=WAL")

	v1Schema := `
CREATE TABLE IF NOT EXISTS host_metrics (
	timestamp INTEGER, cpu_percent REAL, mem_total INTEGER, mem_used INTEGER,
	mem_percent REAL, swap_total INTEGER, swap_used INTEGER,
	load1 REAL, load5 REAL, load15 REAL, uptime REAL
);
CREATE TABLE IF NOT EXISTS container_metrics (
	timestamp INTEGER, project TEXT, service TEXT, cpu_percent REAL,
	mem_usage INTEGER, mem_limit INTEGER, mem_percent REAL,
	net_rx INTEGER, net_tx INTEGER, block_read INTEGER, block_write INTEGER, pids INTEGER
);
CREATE TABLE IF NOT EXISTS disk_metrics (timestamp INTEGER, mountpoint TEXT, device TEXT, total INTEGER, used INTEGER, free INTEGER, percent REAL);
CREATE TABLE IF NOT EXISTS net_metrics (timestamp INTEGER, iface TEXT, rx_bytes INTEGER, tx_bytes INTEGER, rx_packets INTEGER, tx_packets INTEGER, rx_errors INTEGER, tx_errors INTEGER);
CREATE TABLE IF NOT EXISTS logs (timestamp INTEGER, container_id TEXT, container_name TEXT, stream TEXT, message TEXT);
CREATE TABLE IF NOT EXISTS alerts (id INTEGER PRIMARY KEY AUTOINCREMENT, rule_name TEXT, severity TEXT, condition TEXT, instance_key TEXT, fired_at INTEGER, resolved_at INTEGER, message TEXT, acknowledged INTEGER DEFAULT 0);
CREATE TABLE IF NOT EXISTS tracking_state (kind TEXT NOT NULL, name TEXT NOT NULL, UNIQUE(kind, name));
PRAGMA user_version = 1;
`
	if _, err := db.Exec(v1Schema); err != nil {
		db.Close()
		t.Fatal(err)
	}

	ts := time.Now().Unix()
	// Insert rows with empty messages — both level and display_msg will be "".
	for i := 0; i < 5; i++ {
		db.Exec(`INSERT INTO logs (timestamp, container_id, container_name, stream, message) VALUES (?, 'a', 'web', 'stdout', '')`, ts)
	}
	db.Close()

	// This would hang forever without the batchSize break fix.
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// All rows should still exist.
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM logs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Errorf("got %d rows, want 5", count)
	}
}

func TestBackfillLogFieldsFreshDB(t *testing.T) {
	// On a fresh database (no logs table yet), backfill should be a no-op.
	s := testStore(t)

	// user_version should be current (backfill ran but was a no-op).
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Errorf("user_version = %d, want %d", version, currentSchemaVersion)
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

func TestMigrateOldSchema(t *testing.T) {
	// Create a database with the old container_metrics schema (keyed by container ID),
	// then verify OpenStore migrates it to the new schema.
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.Exec("PRAGMA journal_mode=WAL")

	// Old schema: container_metrics with id/name/image/state columns.
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
	project    TEXT    NOT NULL DEFAULT '',
	service    TEXT    NOT NULL DEFAULT '',
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
CREATE TABLE IF NOT EXISTS tracking_state (kind TEXT NOT NULL, name TEXT NOT NULL, UNIQUE(kind, name));
`
	if _, err := db.Exec(oldSchema); err != nil {
		db.Close()
		t.Fatal(err)
	}

	// Insert old-format data.
	ts := time.Now().Unix()
	_, err = db.Exec(`INSERT INTO container_metrics (timestamp, id, name, image, state, project, service, cpu_percent, mem_usage, mem_limit, mem_percent, net_rx, net_tx, block_read, block_write, pids)
		VALUES (?, 'abc123', 'myapp-web-1', 'nginx', 'running', 'myapp', 'web', 42.0, 100000, 200000, 50.0, 1000, 500, 200, 100, 5)`, ts)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	// Non-compose container: no project/service, should use name as service.
	_, err = db.Exec(`INSERT INTO container_metrics (timestamp, id, name, image, state, project, service, cpu_percent, mem_usage, mem_limit, mem_percent, net_rx, net_tx, block_read, block_write, pids)
		VALUES (?, 'def456', 'standalone', 'alpine', 'running', '', '', 10.0, 50000, 100000, 50.0, 0, 0, 0, 0, 1)`, ts)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	// Open via OpenStore which runs migration.
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Verify data was migrated.
	results, err := s.QueryContainerMetrics(t.Context(), ts, ts)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	// Find the compose container.
	var compose, standalone *TimedContainerMetrics
	for i := range results {
		if results[i].Service == "web" {
			compose = &results[i]
		}
		if results[i].Service == "standalone" {
			standalone = &results[i]
		}
	}

	if compose == nil {
		t.Fatal("compose container not found after migration")
	}
	if compose.Project != "myapp" {
		t.Errorf("compose project = %q, want myapp", compose.Project)
	}
	if compose.CPUPercent != 42.0 {
		t.Errorf("compose cpu = %f, want 42.0", compose.CPUPercent)
	}

	if standalone == nil {
		t.Fatal("standalone container not found after migration")
	}
	if standalone.Project != "" {
		t.Errorf("standalone project = %q, want empty", standalone.Project)
	}
	if standalone.CPUPercent != 10.0 {
		t.Errorf("standalone cpu = %f, want 10.0", standalone.CPUPercent)
	}

	// Verify user_version was set.
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Errorf("user_version = %d, want %d", version, currentSchemaVersion)
	}
}

func TestEnableAutoVacuum(t *testing.T) {
	// Create a v2 database (auto_vacuum not yet enabled).
	path := filepath.Join(t.TempDir(), "v2.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.Exec("PRAGMA journal_mode=WAL")

	// v2 schema — minimal tables to pass migration.
	v2Schema := `
CREATE TABLE IF NOT EXISTS host_metrics (
	timestamp INTEGER, cpu_percent REAL, mem_total INTEGER, mem_used INTEGER,
	mem_percent REAL, mem_cached INTEGER DEFAULT 0, mem_free INTEGER DEFAULT 0,
	swap_total INTEGER, swap_used INTEGER,
	load1 REAL, load5 REAL, load15 REAL, uptime REAL
);
CREATE TABLE IF NOT EXISTS container_metrics (
	timestamp INTEGER, project TEXT, service TEXT, cpu_percent REAL,
	mem_usage INTEGER, mem_limit INTEGER, mem_percent REAL,
	net_rx INTEGER, net_tx INTEGER, block_read INTEGER, block_write INTEGER, pids INTEGER
);
CREATE TABLE IF NOT EXISTS disk_metrics (timestamp INTEGER, mountpoint TEXT, device TEXT, total INTEGER, used INTEGER, free INTEGER, percent REAL);
CREATE TABLE IF NOT EXISTS net_metrics (timestamp INTEGER, iface TEXT, rx_bytes INTEGER, tx_bytes INTEGER, rx_packets INTEGER, tx_packets INTEGER, rx_errors INTEGER, tx_errors INTEGER);
CREATE TABLE IF NOT EXISTS logs (timestamp INTEGER, container_id TEXT, container_name TEXT, project TEXT DEFAULT '', service TEXT DEFAULT '', stream TEXT, message TEXT, level TEXT DEFAULT '', display_msg TEXT DEFAULT '');
CREATE TABLE IF NOT EXISTS alerts (id INTEGER PRIMARY KEY AUTOINCREMENT, rule_name TEXT, severity TEXT, condition TEXT, instance_key TEXT, fired_at INTEGER, resolved_at INTEGER, message TEXT, acknowledged INTEGER DEFAULT 0);
CREATE TABLE IF NOT EXISTS tracking_state (kind TEXT NOT NULL, name TEXT NOT NULL, UNIQUE(kind, name));
PRAGMA user_version = 2;
`
	if _, err := db.Exec(v2Schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	// Verify auto_vacuum is 0 (none) before migration.
	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db2.SetMaxOpenConns(1)
	var modeBefore int
	if err := db2.QueryRow("PRAGMA auto_vacuum").Scan(&modeBefore); err != nil {
		db2.Close()
		t.Fatal(err)
	}
	db2.Close()
	if modeBefore != 0 {
		t.Fatalf("auto_vacuum before migration = %d, want 0", modeBefore)
	}

	// OpenStore triggers v2→v3 migration including enableAutoVacuum.
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var mode int
	if err := s.db.QueryRow("PRAGMA auto_vacuum").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 2 {
		t.Errorf("auto_vacuum = %d, want 2 (incremental)", mode)
	}

	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Errorf("user_version = %d, want %d", version, currentSchemaVersion)
	}
}

func TestNewStoreAutoVacuum(t *testing.T) {
	// A fresh database should have auto_vacuum = 2 (incremental).
	s := testStore(t)

	var mode int
	if err := s.db.QueryRow("PRAGMA auto_vacuum").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 2 {
		t.Errorf("auto_vacuum = %d, want 2 (incremental)", mode)
	}
}
