package agent

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
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
CREATE INDEX IF NOT EXISTS idx_container_metrics_ts ON container_metrics(timestamp);

CREATE TABLE IF NOT EXISTS logs (
	timestamp      INTEGER NOT NULL,
	container_id   TEXT    NOT NULL,
	container_name TEXT    NOT NULL,
	stream         TEXT    NOT NULL,
	message        TEXT    NOT NULL
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
`

// Store manages SQLite persistence for metrics and logs.
type Store struct {
	db *sql.DB
}

// OpenStore opens or creates a SQLite database at the given path with WAL mode.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// HostMetrics represents a single host metrics snapshot.
type HostMetrics struct {
	CPUPercent float64
	MemTotal   uint64
	MemUsed    uint64
	MemPercent float64
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
	ID         string
	Name       string
	Image      string
	State      string
	CPUPercent float64
	MemUsage   uint64
	MemLimit   uint64
	MemPercent float64
	NetRx      uint64
	NetTx      uint64
	BlockRead  uint64
	BlockWrite uint64
	PIDs       uint64
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
	Stream        string
	Message       string
}

func (s *Store) InsertHostMetrics(ctx context.Context, ts time.Time, m *HostMetrics) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO host_metrics (timestamp, cpu_percent, mem_total, mem_used, mem_percent, swap_total, swap_used, load1, load5, load15, uptime)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts.Unix(), m.CPUPercent, m.MemTotal, m.MemUsed, m.MemPercent,
		m.SwapTotal, m.SwapUsed, m.Load1, m.Load5, m.Load15, m.Uptime,
	)
	return err
}

func (s *Store) InsertDiskMetrics(ctx context.Context, ts time.Time, disks []DiskMetrics) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO disk_metrics (timestamp, mountpoint, device, total, used, free, percent)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	unix := ts.Unix()
	for _, d := range disks {
		if _, err := stmt.ExecContext(ctx, unix, d.Mountpoint, d.Device, d.Total, d.Used, d.Free, d.Percent); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) InsertNetMetrics(ctx context.Context, ts time.Time, nets []NetMetrics) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO net_metrics (timestamp, iface, rx_bytes, tx_bytes, rx_packets, tx_packets, rx_errors, tx_errors)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	unix := ts.Unix()
	for _, n := range nets {
		if _, err := stmt.ExecContext(ctx, unix, n.Iface, n.RxBytes, n.TxBytes, n.RxPackets, n.TxPackets, n.RxErrors, n.TxErrors); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) InsertContainerMetrics(ctx context.Context, ts time.Time, containers []ContainerMetrics) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO container_metrics (timestamp, id, name, image, state, cpu_percent, mem_usage, mem_limit, mem_percent, net_rx, net_tx, block_read, block_write, pids)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	unix := ts.Unix()
	for _, c := range containers {
		if _, err := stmt.ExecContext(ctx, unix, c.ID, c.Name, c.Image, c.State,
			c.CPUPercent, c.MemUsage, c.MemLimit, c.MemPercent,
			c.NetRx, c.NetTx, c.BlockRead, c.BlockWrite, c.PIDs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) InsertLogs(ctx context.Context, entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO logs (timestamp, container_id, container_name, stream, message)
		 VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, e.Timestamp.Unix(), e.ContainerID, e.ContainerName, e.Stream, e.Message); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) InsertAlert(ctx context.Context, a *Alert) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO alerts (rule_name, severity, condition, instance_key, fired_at, message)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		a.RuleName, a.Severity, a.Condition, a.InstanceKey, a.FiredAt.Unix(), a.Message,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ResolveAlert(ctx context.Context, id int64, resolvedAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE alerts SET resolved_at = ? WHERE id = ?`,
		resolvedAt.Unix(), id,
	)
	return err
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
	Start        int64  // unix seconds
	End          int64  // unix seconds
	ContainerIDs []string
	Stream       string
	Search       string
	Limit        int
}

func (s *Store) QueryHostMetrics(ctx context.Context, start, end int64) ([]TimedHostMetrics, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT timestamp, cpu_percent, mem_total, mem_used, mem_percent, swap_total, swap_used, load1, load5, load15, uptime
		 FROM host_metrics WHERE timestamp >= ? AND timestamp <= ? ORDER BY timestamp`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TimedHostMetrics
	for rows.Next() {
		var t TimedHostMetrics
		var ts int64
		if err := rows.Scan(&ts, &t.CPUPercent, &t.MemTotal, &t.MemUsed, &t.MemPercent,
			&t.SwapTotal, &t.SwapUsed, &t.Load1, &t.Load5, &t.Load15, &t.Uptime); err != nil {
			return nil, err
		}
		t.Timestamp = time.Unix(ts, 0)
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *Store) QueryDiskMetrics(ctx context.Context, start, end int64) ([]TimedDiskMetrics, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT timestamp, mountpoint, device, total, used, free, percent
		 FROM disk_metrics WHERE timestamp >= ? AND timestamp <= ? ORDER BY timestamp`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TimedDiskMetrics
	for rows.Next() {
		var t TimedDiskMetrics
		var ts int64
		if err := rows.Scan(&ts, &t.Mountpoint, &t.Device, &t.Total, &t.Used, &t.Free, &t.Percent); err != nil {
			return nil, err
		}
		t.Timestamp = time.Unix(ts, 0)
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *Store) QueryNetMetrics(ctx context.Context, start, end int64) ([]TimedNetMetrics, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT timestamp, iface, rx_bytes, tx_bytes, rx_packets, tx_packets, rx_errors, tx_errors
		 FROM net_metrics WHERE timestamp >= ? AND timestamp <= ? ORDER BY timestamp`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TimedNetMetrics
	for rows.Next() {
		var t TimedNetMetrics
		var ts int64
		if err := rows.Scan(&ts, &t.Iface, &t.RxBytes, &t.TxBytes, &t.RxPackets, &t.TxPackets, &t.RxErrors, &t.TxErrors); err != nil {
			return nil, err
		}
		t.Timestamp = time.Unix(ts, 0)
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *Store) QueryContainerMetrics(ctx context.Context, start, end int64) ([]TimedContainerMetrics, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT timestamp, id, name, image, state, cpu_percent, mem_usage, mem_limit, mem_percent, net_rx, net_tx, block_read, block_write, pids
		 FROM container_metrics WHERE timestamp >= ? AND timestamp <= ? ORDER BY timestamp`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TimedContainerMetrics
	for rows.Next() {
		var t TimedContainerMetrics
		var ts int64
		if err := rows.Scan(&ts, &t.ID, &t.Name, &t.Image, &t.State,
			&t.CPUPercent, &t.MemUsage, &t.MemLimit, &t.MemPercent,
			&t.NetRx, &t.NetTx, &t.BlockRead, &t.BlockWrite, &t.PIDs); err != nil {
			return nil, err
		}
		t.Timestamp = time.Unix(ts, 0)
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *Store) QueryLogs(ctx context.Context, f LogFilter) ([]LogEntry, error) {
	query := `SELECT timestamp, container_id, container_name, stream, message FROM logs WHERE timestamp >= ? AND timestamp <= ?`
	args := []any{f.Start, f.End}

	if len(f.ContainerIDs) == 1 {
		query += ` AND container_id = ?`
		args = append(args, f.ContainerIDs[0])
	} else if len(f.ContainerIDs) > 1 {
		placeholders := make([]string, len(f.ContainerIDs))
		for i, id := range f.ContainerIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		query += ` AND container_id IN (` + joinStrings(placeholders, ",") + `)`
	}
	if f.Stream != "" {
		query += ` AND stream = ?`
		args = append(args, f.Stream)
	}
	if f.Search != "" {
		query += ` AND message LIKE ? ESCAPE '\'`
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Search)
		args = append(args, "%"+escaped+"%")
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 1000
	}
	query += ` ORDER BY timestamp DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []LogEntry
	for rows.Next() {
		var e LogEntry
		var ts int64
		if err := rows.Scan(&ts, &e.ContainerID, &e.ContainerName, &e.Stream, &e.Message); err != nil {
			return nil, err
		}
		e.Timestamp = time.Unix(ts, 0)
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *Store) QueryAlerts(ctx context.Context, start, end int64) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, rule_name, severity, condition, instance_key, fired_at, resolved_at, message, acknowledged
		 FROM alerts WHERE fired_at >= ? AND fired_at <= ? ORDER BY fired_at DESC`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Alert
	for rows.Next() {
		var a Alert
		var firedAt int64
		var resolvedAt *int64
		var ack int
		if err := rows.Scan(&a.ID, &a.RuleName, &a.Severity, &a.Condition, &a.InstanceKey,
			&firedAt, &resolvedAt, &a.Message, &ack); err != nil {
			return nil, err
		}
		a.FiredAt = time.Unix(firedAt, 0)
		if resolvedAt != nil {
			t := time.Unix(*resolvedAt, 0)
			a.ResolvedAt = &t
		}
		a.Acknowledged = ack != 0
		result = append(result, a)
	}
	return result, rows.Err()
}

// AckAlert marks an alert as acknowledged.
func (s *Store) AckAlert(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE alerts SET acknowledged = 1 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("alert %d not found", id)
	}
	return nil
}

func joinStrings(s []string, sep string) string {
	result := ""
	for i, v := range s {
		if i > 0 {
			result += sep
		}
		result += v
	}
	return result
}

// Prune deletes data older than the retention period.
func (s *Store) Prune(ctx context.Context, retentionDays int) error {
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).Unix()

	tables := []string{"host_metrics", "disk_metrics", "net_metrics", "container_metrics", "logs"}
	for _, table := range tables {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE timestamp < ?", table), cutoff); err != nil {
			return fmt.Errorf("prune %s: %w", table, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM alerts WHERE fired_at < ?", cutoff); err != nil {
		return fmt.Errorf("prune alerts: %w", err)
	}
	return nil
}
