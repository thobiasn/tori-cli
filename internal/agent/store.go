package agent

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"modernc.org/sqlite"
)

func init() {
	// Cache the last compiled regex — SetMaxOpenConns(1) guarantees
	// single-threaded access so no mutex is needed.
	var lastPattern string
	var lastRe *regexp.Regexp

	sqlite.MustRegisterDeterministicScalarFunction("regexp", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		pattern, ok1 := args[0].(string)
		value, ok2 := args[1].(string)
		if !ok1 || !ok2 {
			return int64(0), nil
		}
		if pattern != lastPattern {
			var err error
			lastRe, err = regexp.Compile(pattern)
			if err != nil {
				return int64(0), nil
			}
			lastPattern = pattern
		}
		if lastRe.MatchString(value) {
			return int64(1), nil
		}
		return int64(0), nil
	})
}

// currentSchemaVersion is incremented when the schema changes in a way that
// requires data migration (not just adding columns).
const currentSchemaVersion = 3

const schema = `
CREATE TABLE IF NOT EXISTS host_metrics (
	timestamp    INTEGER NOT NULL,
	cpu_percent  REAL    NOT NULL,
	mem_total    INTEGER NOT NULL,
	mem_used     INTEGER NOT NULL,
	mem_percent  REAL    NOT NULL,
	mem_cached   INTEGER NOT NULL DEFAULT 0,
	mem_free     INTEGER NOT NULL DEFAULT 0,
	swap_total   INTEGER NOT NULL,
	swap_used    INTEGER NOT NULL,
	load1        REAL    NOT NULL,
	load5        REAL    NOT NULL,
	load15       REAL    NOT NULL,
	uptime       REAL    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_host_metrics_ts ON host_metrics(timestamp);

CREATE TABLE IF NOT EXISTS disk_metrics (
	timestamp  INTEGER NOT NULL,
	mountpoint TEXT    NOT NULL,
	device     TEXT    NOT NULL,
	total      INTEGER NOT NULL,
	used       INTEGER NOT NULL,
	free       INTEGER NOT NULL,
	percent    REAL    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_disk_metrics_ts ON disk_metrics(timestamp);

CREATE TABLE IF NOT EXISTS net_metrics (
	timestamp  INTEGER NOT NULL,
	iface      TEXT    NOT NULL,
	rx_bytes   INTEGER NOT NULL,
	tx_bytes   INTEGER NOT NULL,
	rx_packets INTEGER NOT NULL,
	tx_packets INTEGER NOT NULL,
	rx_errors  INTEGER NOT NULL,
	tx_errors  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_net_metrics_ts ON net_metrics(timestamp);

CREATE TABLE IF NOT EXISTS container_metrics (
	timestamp   INTEGER NOT NULL,
	project     TEXT    NOT NULL,
	service     TEXT    NOT NULL,
	cpu_percent REAL    NOT NULL,
	mem_usage   INTEGER NOT NULL,
	mem_limit   INTEGER NOT NULL,
	mem_percent REAL    NOT NULL,
	net_rx      INTEGER NOT NULL,
	net_tx      INTEGER NOT NULL,
	block_read  INTEGER NOT NULL,
	block_write INTEGER NOT NULL,
	pids        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_container_metrics_svc ON container_metrics(project, service, timestamp);

CREATE TABLE IF NOT EXISTS logs (
	timestamp      INTEGER NOT NULL,
	container_id   TEXT    NOT NULL,
	container_name TEXT    NOT NULL,
	stream         TEXT    NOT NULL,
	message        TEXT    NOT NULL,
	level          TEXT    NOT NULL DEFAULT '',
	display_msg    TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_logs_ts ON logs(timestamp);
CREATE INDEX IF NOT EXISTS idx_logs_container_ts ON logs(container_id, timestamp);

CREATE TABLE IF NOT EXISTS alerts (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	rule_name    TEXT    NOT NULL,
	severity     TEXT    NOT NULL,
	condition    TEXT    NOT NULL,
	instance_key TEXT    NOT NULL,
	fired_at     INTEGER NOT NULL,
	resolved_at  INTEGER,
	message      TEXT    NOT NULL,
	acknowledged INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_alerts_fired ON alerts(fired_at);
CREATE INDEX IF NOT EXISTS idx_alerts_unresolved ON alerts(fired_at) WHERE resolved_at IS NULL;

CREATE TABLE IF NOT EXISTS tracking_state (
	kind TEXT NOT NULL,
	name TEXT NOT NULL,
	UNIQUE(kind, name)
);
`

// Store manages SQLite persistence for metrics and logs.
type Store struct {
	db   *sql.DB
	path string
}

// OpenStore opens or creates a SQLite database at the given path with WAL mode.
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// Limit SQLite page cache to ~2MB (negative = KB).
	if _, err := db.Exec("PRAGMA cache_size = -2000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set cache_size: %w", err)
	}

	// Enable incremental auto-vacuum for new databases. On existing databases
	// this PRAGMA has no effect without a VACUUM (handled by migration).
	if _, err := db.Exec("PRAGMA auto_vacuum = 2"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set auto_vacuum: %w", err)
	}

	s := &Store{db: db, path: path}

	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	// Restrict database file permissions to owner-only.
	if err := os.Chmod(path, 0o600); err != nil {
		slog.Warn("failed to set database file permissions", "error", err)
	}

	s.ensureColumns()
	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// migrate handles schema migrations using PRAGMA user_version for tracking.
func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	if version >= currentSchemaVersion {
		return nil
	}

	// Version 0 → 1: Migrate container_metrics from per-container-ID schema
	// to synthetic identity (project, service) schema.
	if version < 1 {
		if err := s.migrateContainerMetricsV1(); err != nil {
			return fmt.Errorf("migrate container_metrics v1: %w", err)
		}
	}

	// Version 1 → 2: Backfill level and display_msg for existing log rows.
	if version < 2 {
		if err := s.backfillLogFields(); err != nil {
			return fmt.Errorf("backfill log fields: %w", err)
		}
	}

	// Version 2 → 3: Enable incremental auto-vacuum.
	if version < 3 {
		if err := s.enableAutoVacuum(); err != nil {
			slog.Warn("auto_vacuum migration skipped", "error", err)
		}
	}

	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", currentSchemaVersion)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	return nil
}

// migrateContainerMetricsV1 converts the old container_metrics table (keyed by
// raw container ID with 21 columns) to the new schema (keyed by synthetic
// identity with 12 columns). No-op if the old table doesn't exist.
func (s *Store) migrateContainerMetricsV1() error {
	// Check if the old table exists and has the 'id' column.
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('container_metrics') WHERE name = 'id'`).Scan(&count)
	if err != nil || count == 0 {
		return nil // Fresh database or already migrated.
	}

	slog.Info("migrating container_metrics to synthetic identity schema")

	// Ensure project/service columns exist on old table (may be missing from very old DBs).
	for _, stmt := range []string{
		"ALTER TABLE container_metrics ADD COLUMN project TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE container_metrics ADD COLUMN service TEXT NOT NULL DEFAULT ''",
	} {
		_, err := s.db.Exec(stmt)
		if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add column: %w", err)
		}
	}

	// Create new table, copy data, swap.
	migration := `
		CREATE TABLE container_metrics_v2 (
			timestamp   INTEGER NOT NULL,
			project     TEXT    NOT NULL,
			service     TEXT    NOT NULL,
			cpu_percent REAL    NOT NULL,
			mem_usage   INTEGER NOT NULL,
			mem_limit   INTEGER NOT NULL,
			mem_percent REAL    NOT NULL,
			net_rx      INTEGER NOT NULL,
			net_tx      INTEGER NOT NULL,
			block_read  INTEGER NOT NULL,
			block_write INTEGER NOT NULL,
			pids        INTEGER NOT NULL
		);
		INSERT INTO container_metrics_v2
			SELECT timestamp,
				COALESCE(NULLIF(project, ''), ''),
				CASE WHEN service != '' THEN service ELSE name END,
				cpu_percent, mem_usage, mem_limit, mem_percent,
				net_rx, net_tx, block_read, block_write, pids
			FROM container_metrics;
		DROP TABLE container_metrics;
		ALTER TABLE container_metrics_v2 RENAME TO container_metrics;
	`
	if _, err := s.db.Exec(migration); err != nil {
		return fmt.Errorf("migrate table: %w", err)
	}

	slog.Info("container_metrics migration complete")
	return nil
}

// backfillLogFields parses level and display_msg from existing log rows that
// were ingested before these columns existed. Runs once during the v1→v2 migration.
func (s *Store) backfillLogFields() error {
	// Skip on fresh databases where the logs table doesn't exist yet.
	var tableCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='logs'`).Scan(&tableCount); err != nil {
		return fmt.Errorf("check logs table: %w", err)
	}
	if tableCount == 0 {
		return nil
	}

	// Ensure columns exist before backfilling (ensureColumns hasn't run yet).
	for _, stmt := range []string{
		"ALTER TABLE logs ADD COLUMN level TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE logs ADD COLUMN display_msg TEXT NOT NULL DEFAULT ''",
	} {
		_, err := s.db.Exec(stmt)
		if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add column: %w", err)
		}
	}

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM logs WHERE level = '' AND display_msg = ''`).Scan(&total); err != nil {
		return fmt.Errorf("count rows: %w", err)
	}
	if total == 0 {
		return nil
	}

	slog.Info("backfilling log level and display_msg", "rows", total)

	const batchSize = 1000
	for {
		rows, err := s.db.Query(
			`SELECT rowid, message FROM logs WHERE level = '' AND display_msg = '' LIMIT ?`,
			batchSize,
		)
		if err != nil {
			return fmt.Errorf("select batch: %w", err)
		}

		type update struct {
			rowid      int64
			level      string
			displayMsg string
		}
		var updates []update
		for rows.Next() {
			var rowid int64
			var msg string
			if err := rows.Scan(&rowid, &msg); err != nil {
				rows.Close()
				return fmt.Errorf("scan row: %w", err)
			}
			lvl, dm := ParseLogFields(msg)
			updates = append(updates, update{
				rowid:      rowid,
				level:      lvl,
				displayMsg: dm,
			})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate log rows: %w", err)
		}

		if len(updates) == 0 {
			break
		}

		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		stmt, err := tx.Prepare(`UPDATE logs SET level = ?, display_msg = ? WHERE rowid = ?`)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("prepare update: %w", err)
		}
		for _, u := range updates {
			if _, err := stmt.Exec(u.level, u.displayMsg, u.rowid); err != nil {
				stmt.Close()
				tx.Rollback()
				return fmt.Errorf("update row: %w", err)
			}
		}
		stmt.Close()
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit batch: %w", err)
		}

		// If we got fewer than a full batch, we've processed everything.
		// This also handles rows where parsed level and display_msg are both
		// empty — they'd re-match the WHERE clause forever otherwise.
		if len(updates) < batchSize {
			break
		}
	}

	slog.Info("log backfill complete")
	return nil
}

// enableAutoVacuum switches an existing database to incremental auto-vacuum mode.
// This requires a one-time VACUUM which rewrites the entire file. Skipped if
// there isn't enough free disk space (2× the DB file size).
func (s *Store) enableAutoVacuum() error {
	var mode int
	if err := s.db.QueryRow("PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return fmt.Errorf("read auto_vacuum: %w", err)
	}
	if mode == 2 {
		return nil // already incremental
	}

	info, err := os.Stat(s.path)
	if err != nil {
		return fmt.Errorf("stat db: %w", err)
	}
	dbSize := info.Size()

	var fs syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(s.path), &fs); err != nil {
		return fmt.Errorf("statfs: %w", err)
	}
	freeBytes := int64(fs.Bavail) * int64(fs.Bsize)

	if freeBytes < 2*dbSize {
		slog.Warn("skipping auto_vacuum: insufficient free space",
			"db_size", dbSize, "free", freeBytes)
		return nil
	}

	slog.Info("enabling auto_vacuum, running one-time VACUUM (this may take a moment for large databases)")

	if _, err := s.db.Exec("PRAGMA auto_vacuum = 2"); err != nil {
		return fmt.Errorf("set auto_vacuum: %w", err)
	}
	if _, err := s.db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}

	return nil
}

// ensureColumns adds columns that may be missing from older databases.
// Silently ignores "duplicate column name" errors (column already exists).
func (s *Store) ensureColumns() {
	migrations := []string{
		"ALTER TABLE host_metrics ADD COLUMN mem_cached INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE host_metrics ADD COLUMN mem_free INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE logs ADD COLUMN project TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE logs ADD COLUMN service TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE logs ADD COLUMN level TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE logs ADD COLUMN display_msg TEXT NOT NULL DEFAULT ''",
	}
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_logs_svc ON logs(project, service, timestamp)",
		"CREATE INDEX IF NOT EXISTS idx_logs_container_level_ts ON logs(container_id, level, timestamp)",
		"CREATE INDEX IF NOT EXISTS idx_logs_svc_level ON logs(project, service, level, timestamp)",
	}
	for _, stmt := range migrations {
		_, err := s.db.Exec(stmt)
		if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			slog.Warn("migration failed", "error", err)
		}
	}
	for _, stmt := range indexes {
		if _, err := s.db.Exec(stmt); err != nil {
			slog.Warn("index creation failed", "error", err)
		}
	}
}

// --- Metric types ---

// HostMetrics represents a single host metrics snapshot.
type HostMetrics struct {
	CPUPercent float64
	CPUs       int // number of logical CPU cores (live-only, not persisted)
	MemTotal   uint64
	MemUsed    uint64
	MemPercent float64
	MemCached  uint64
	MemFree    uint64
	SwapTotal  uint64
	SwapUsed   uint64
	Load1      float64
	Load5      float64
	Load15     float64
	Uptime     float64
}

// DiskMetrics represents disk usage for a single mountpoint.
type DiskMetrics struct {
	Mountpoint string
	Device     string
	Total      uint64
	Used       uint64
	Free       uint64
	Percent    float64
}

// NetMetrics represents network counters for a single interface.
type NetMetrics struct {
	Iface     string
	RxBytes   uint64
	TxBytes   uint64
	RxPackets uint64
	TxPackets uint64
	RxErrors  uint64
	TxErrors  uint64
}

// ContainerMetrics represents stats for a single Docker container.
type ContainerMetrics struct {
	ID           string
	Name         string
	Image        string
	State        string
	Project      string
	Service      string
	Health       string
	StartedAt    int64
	RestartCount int
	ExitCode     int
	CPUPercent   float64
	CPULimit     float64 // configured CPU limit in cores (0 = no limit, live-only)
	MemUsage     uint64
	MemLimit     uint64  // configured memory limit in bytes (0 = no limit)
	MemPercent   float64
	NetRx        uint64
	NetTx        uint64
	BlockRead    uint64
	BlockWrite   uint64
	PIDs         uint64
	DiskUsage    uint64
}

// Alert represents a fired alert stored in the database.
type Alert struct {
	ID           int64
	RuleName     string
	Severity     string
	Condition    string
	InstanceKey  string
	FiredAt      time.Time
	ResolvedAt   *time.Time
	Message      string
	Acknowledged bool
}

// LogEntry represents a single log line from a container.
type LogEntry struct {
	Timestamp     time.Time
	ContainerID   string
	ContainerName string
	Project       string
	Service       string
	Stream        string
	Message       string
	Level         string // "ERR", "WARN", "INFO", "DBUG", or ""
	DisplayMsg    string // clean message extracted from JSON/logfmt, or raw
}

// --- Query types ---

// TimedHostMetrics is a HostMetrics with a timestamp.
type TimedHostMetrics struct {
	Timestamp time.Time
	HostMetrics
}

// TimedDiskMetrics is a DiskMetrics with a timestamp.
type TimedDiskMetrics struct {
	Timestamp time.Time
	DiskMetrics
}

// TimedNetMetrics is a NetMetrics with a timestamp.
type TimedNetMetrics struct {
	Timestamp time.Time
	NetMetrics
}

// TimedContainerMetrics is a ContainerMetrics with a timestamp.
type TimedContainerMetrics struct {
	Timestamp time.Time
	ContainerMetrics
}

// LogFilter specifies query parameters for log retrieval.
type LogFilter struct {
	Start         int64  // unix seconds
	End           int64  // unix seconds
	ContainerIDs  []string
	Project       string // service identity: project
	Service       string // service identity: service (or container name for non-compose)
	Search        string
	SearchIsRegex bool   // true = Search is valid regex, false = use LIKE
	Level         string // "ERR", "WARN", "INFO", "DBUG"
	Limit         int
}
