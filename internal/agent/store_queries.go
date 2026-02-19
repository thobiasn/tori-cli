package agent

import (
	"context"
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
	"time"
)

func (s *Store) InsertHostMetrics(ctx context.Context, ts time.Time, m *HostMetrics) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO host_metrics (timestamp, cpu_percent, mem_total, mem_used, mem_percent, mem_cached, mem_free, swap_total, swap_used, load1, load5, load15, uptime)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts.Unix(), m.CPUPercent, m.MemTotal, m.MemUsed, m.MemPercent,
		m.MemCached, m.MemFree,
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
		`INSERT INTO container_metrics (timestamp, project, service, cpu_percent, mem_usage, mem_limit, mem_percent, net_rx, net_tx, block_read, block_write, pids)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	unix := ts.Unix()
	for _, c := range containers {
		if _, err := stmt.ExecContext(ctx, unix, c.Project, c.Service,
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
		`INSERT INTO logs (timestamp, container_id, container_name, project, service, stream, message, level, display_msg)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, e.Timestamp.Unix(), e.ContainerID, e.ContainerName, e.Project, e.Service, e.Stream, e.Message, e.Level, e.DisplayMsg); err != nil {
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

// ResolveOrphanedAlerts resolves all alerts with resolved_at IS NULL.
// Called on agent startup to clean up alerts left by a previous crash.
func (s *Store) ResolveOrphanedAlerts(ctx context.Context, resolvedAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE alerts SET resolved_at = ? WHERE resolved_at IS NULL`,
		resolvedAt.Unix(),
	)
	return err
}

// QueryFiringAlerts returns all currently firing (unresolved) alerts.
func (s *Store) QueryFiringAlerts(ctx context.Context) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, rule_name, severity, condition, instance_key, fired_at, resolved_at, message, acknowledged
		 FROM alerts WHERE resolved_at IS NULL ORDER BY fired_at DESC LIMIT 1000`)
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

// --- Query methods ---

// QueryHostMetricsGrouped returns host metrics aggregated into time buckets.
// Each bucket contains MAX values. Returns at most (end-start)/bucketDur rows,
// far fewer than the raw query. Used for downsampled historical views.
func (s *Store) QueryHostMetricsGrouped(ctx context.Context, start, end, bucketDur int64) ([]TimedHostMetrics, error) {
	if bucketDur <= 0 {
		bucketDur = 1
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT ? + ((timestamp - ?) / ?) * ? AS bucket_ts,
		 MAX(cpu_percent), MAX(mem_total), MAX(mem_used), MAX(mem_percent),
		 MAX(mem_cached), MAX(mem_free), MAX(swap_total), MAX(swap_used),
		 MAX(load1), MAX(load5), MAX(load15), MAX(uptime)
		 FROM host_metrics WHERE timestamp >= ? AND timestamp <= ?
		 GROUP BY (timestamp - ?) / ?
		 ORDER BY bucket_ts`,
		start, start, bucketDur, bucketDur,
		start, end,
		start, bucketDur)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TimedHostMetrics
	for rows.Next() {
		var t TimedHostMetrics
		var ts int64
		if err := rows.Scan(&ts, &t.CPUPercent, &t.MemTotal, &t.MemUsed, &t.MemPercent,
			&t.MemCached, &t.MemFree,
			&t.SwapTotal, &t.SwapUsed, &t.Load1, &t.Load5, &t.Load15, &t.Uptime); err != nil {
			return nil, err
		}
		t.Timestamp = time.Unix(ts, 0)
		result = append(result, t)
	}
	return result, rows.Err()
}

// QueryContainerMetricsGrouped returns container metrics aggregated into time
// buckets per service identity. Used for downsampled historical views.
func (s *Store) QueryContainerMetricsGrouped(ctx context.Context, start, end, bucketDur int64, filters ...ContainerMetricsFilter) ([]TimedContainerMetrics, error) {
	if bucketDur <= 0 {
		bucketDur = 1
	}
	query := `SELECT ? + ((timestamp - ?) / ?) * ? AS bucket_ts,
		 project, service,
		 MAX(cpu_percent), MAX(mem_usage), MAX(mem_limit), MAX(mem_percent),
		 MAX(net_rx), MAX(net_tx), MAX(block_read), MAX(block_write), MAX(pids)
		 FROM container_metrics WHERE timestamp >= ? AND timestamp <= ?`
	args := []any{start, start, bucketDur, bucketDur, start, end}

	if len(filters) > 0 {
		f := filters[0]
		if f.Service != "" {
			if f.Project != "" {
				query += ` AND project = ? AND service = ?`
				args = append(args, f.Project, f.Service)
			} else {
				query += ` AND service = ?`
				args = append(args, f.Service)
			}
		} else if f.Project != "" {
			query += ` AND project = ?`
			args = append(args, f.Project)
		}
	}
	query += ` GROUP BY (timestamp - ?) / ?, project, service ORDER BY project, service, bucket_ts`
	args = append(args, start, bucketDur)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TimedContainerMetrics
	for rows.Next() {
		var t TimedContainerMetrics
		var ts int64
		if err := rows.Scan(&ts, &t.Project, &t.Service,
			&t.CPUPercent, &t.MemUsage, &t.MemLimit, &t.MemPercent,
			&t.NetRx, &t.NetTx, &t.BlockRead, &t.BlockWrite, &t.PIDs); err != nil {
			return nil, err
		}
		t.Timestamp = time.Unix(ts, 0)
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *Store) QueryHostMetrics(ctx context.Context, start, end int64) ([]TimedHostMetrics, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT timestamp, cpu_percent, mem_total, mem_used, mem_percent, mem_cached, mem_free, swap_total, swap_used, load1, load5, load15, uptime
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
			&t.MemCached, &t.MemFree,
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

// ContainerMetricsFilter specifies optional service identity filters for
// container metric queries. Zero value means no filtering (return all).
type ContainerMetricsFilter struct {
	Project string
	Service string
}

func (s *Store) QueryContainerMetrics(ctx context.Context, start, end int64, filters ...ContainerMetricsFilter) ([]TimedContainerMetrics, error) {
	query := `SELECT timestamp, project, service, cpu_percent, mem_usage, mem_limit, mem_percent, net_rx, net_tx, block_read, block_write, pids
		 FROM container_metrics WHERE timestamp >= ? AND timestamp <= ?`
	args := []any{start, end}

	if len(filters) > 0 {
		f := filters[0]
		if f.Service != "" {
			if f.Project != "" {
				query += ` AND project = ? AND service = ?`
				args = append(args, f.Project, f.Service)
			} else {
				query += ` AND service = ?`
				args = append(args, f.Service)
			}
		} else if f.Project != "" {
			query += ` AND project = ?`
			args = append(args, f.Project)
		}
	}
	query += ` ORDER BY timestamp`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TimedContainerMetrics
	for rows.Next() {
		var t TimedContainerMetrics
		var ts int64
		if err := rows.Scan(&ts, &t.Project, &t.Service,
			&t.CPUPercent, &t.MemUsage, &t.MemLimit, &t.MemPercent,
			&t.NetRx, &t.NetTx, &t.BlockRead, &t.BlockWrite, &t.PIDs); err != nil {
			return nil, err
		}
		t.Timestamp = time.Unix(ts, 0)
		result = append(result, t)
	}
	return result, rows.Err()
}

// logScopeFilter appends WHERE clauses for service/project/container identity
// to the query. Shared by CountLogs and QueryLogs to keep filter logic in sync.
func logScopeFilter(query string, args []any, f LogFilter) (string, []any) {
	if f.Service != "" {
		if f.Project != "" {
			query += ` AND project = ? AND service = ?`
			args = append(args, f.Project, f.Service)
		} else {
			query += ` AND service = ?`
			args = append(args, f.Service)
		}
	} else if f.Project != "" {
		query += ` AND project = ?`
		args = append(args, f.Project)
	} else if len(f.ContainerIDs) == 1 {
		query += ` AND container_id = ?`
		args = append(args, f.ContainerIDs[0])
	} else if len(f.ContainerIDs) > 1 {
		placeholders := make([]string, len(f.ContainerIDs))
		for i, id := range f.ContainerIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		query += ` AND container_id IN (` + strings.Join(placeholders, ",") + `)`
	}
	return query, args
}

// CountLogs returns the total number of log entries matching the scope filter
// (container/project + time range). Search, Level, and Limit are excluded so
// the count represents the total scope, not the filtered subset.
func (s *Store) CountLogs(ctx context.Context, f LogFilter) (int, error) {
	query := `SELECT COUNT(*) FROM logs WHERE timestamp >= ? AND timestamp <= ?`
	args := []any{f.Start, f.End}
	query, args = logScopeFilter(query, args, f)

	var count int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}

func (s *Store) QueryLogs(ctx context.Context, f LogFilter) ([]LogEntry, error) {
	query := `SELECT timestamp, container_id, container_name, project, service, stream, message, level, display_msg FROM logs WHERE timestamp >= ? AND timestamp <= ?`
	args := []any{f.Start, f.End}
	query, args = logScopeFilter(query, args, f)

	if f.Level != "" {
		query += ` AND level = ?`
		args = append(args, f.Level)
	}
	if f.Search != "" {
		if _, err := regexp.Compile(f.Search); err == nil {
			query += ` AND message REGEXP ?`
			args = append(args, "(?i)"+f.Search)
		} else {
			query += ` AND message LIKE ? ESCAPE '\'`
			escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Search)
			args = append(args, "%"+escaped+"%")
		}
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
		if err := rows.Scan(&ts, &e.ContainerID, &e.ContainerName, &e.Project, &e.Service, &e.Stream, &e.Message, &e.Level, &e.DisplayMsg); err != nil {
			return nil, err
		}
		e.Timestamp = time.Unix(ts, 0)
		result = append(result, e)
	}
	return result, rows.Err()
}

const maxAlertResults = 10000

func (s *Store) QueryAlerts(ctx context.Context, start, end int64) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, rule_name, severity, condition, instance_key, fired_at, resolved_at, message, acknowledged
		 FROM alerts WHERE fired_at >= ? AND fired_at <= ? ORDER BY fired_at DESC LIMIT ?`, start, end, maxAlertResults)
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

// SaveTracking persists the current tracking state (full snapshot).
func (s *Store) SaveTracking(ctx context.Context, containers []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM tracking_state"); err != nil {
		return err
	}

	stmt, err := tx.PrepareContext(ctx, "INSERT INTO tracking_state (kind, name) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, name := range containers {
		if _, err := stmt.ExecContext(ctx, "container", name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadTracking loads the persisted tracking state.
func (s *Store) LoadTracking(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT name FROM tracking_state WHERE kind = 'container'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var containers []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		containers = append(containers, name)
	}
	return containers, rows.Err()
}

// pruneBatchSize limits the number of rows deleted per batch to avoid long-running
// transactions that block other database operations (inserts, queries).
const pruneBatchSize = 5000

// Prune deletes data older than the retention period in batches.
func (s *Store) Prune(ctx context.Context, retentionDays int) error {
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).Unix()

	tables := []string{"host_metrics", "disk_metrics", "net_metrics", "container_metrics", "logs"}
	for _, table := range tables {
		if err := s.pruneTable(ctx, table, "timestamp", cutoff); err != nil {
			return fmt.Errorf("prune %s: %w", table, err)
		}
	}
	if err := s.pruneTable(ctx, "alerts", "fired_at", cutoff); err != nil {
		return fmt.Errorf("prune alerts: %w", err)
	}

	// Checkpoint WAL to reclaim file space, then ask Go to release memory.
	s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
	debug.FreeOSMemory()

	return nil
}

// pruneTable deletes rows where column < cutoff in batches of pruneBatchSize.
func (s *Store) pruneTable(ctx context.Context, table, column string, cutoff int64) error {
	query := fmt.Sprintf(
		"DELETE FROM %s WHERE rowid IN (SELECT rowid FROM %s WHERE %s < ? LIMIT ?)",
		table, table, column,
	)
	for {
		res, err := s.db.ExecContext(ctx, query, cutoff, pruneBatchSize)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n < pruneBatchSize {
			return nil
		}
	}
}
