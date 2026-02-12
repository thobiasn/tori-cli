package agent

import (
	"context"
	"fmt"
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
		`INSERT INTO container_metrics (timestamp, id, name, image, state, project, service, health, started_at, restart_count, exit_code, cpu_percent, mem_usage, mem_limit, mem_percent, net_rx, net_tx, block_read, block_write, pids, disk_usage)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	unix := ts.Unix()
	for _, c := range containers {
		if _, err := stmt.ExecContext(ctx, unix, c.ID, c.Name, c.Image, c.State,
			c.Project, c.Service,
			c.Health, c.StartedAt, c.RestartCount, c.ExitCode,
			c.CPUPercent, c.MemUsage, c.MemLimit, c.MemPercent,
			c.NetRx, c.NetTx, c.BlockRead, c.BlockWrite, c.PIDs, c.DiskUsage); err != nil {
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
		`INSERT INTO logs (timestamp, container_id, container_name, project, service, stream, message)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, e.Timestamp.Unix(), e.ContainerID, e.ContainerName, e.Project, e.Service, e.Stream, e.Message); err != nil {
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

// --- Query methods ---

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
	query := `SELECT timestamp, id, name, image, state, project, service, health, started_at, restart_count, exit_code, cpu_percent, mem_usage, mem_limit, mem_percent, net_rx, net_tx, block_read, block_write, pids, disk_usage
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
		if err := rows.Scan(&ts, &t.ID, &t.Name, &t.Image, &t.State,
			&t.Project, &t.Service,
			&t.Health, &t.StartedAt, &t.RestartCount, &t.ExitCode,
			&t.CPUPercent, &t.MemUsage, &t.MemLimit, &t.MemPercent,
			&t.NetRx, &t.NetTx, &t.BlockRead, &t.BlockWrite, &t.PIDs, &t.DiskUsage); err != nil {
			return nil, err
		}
		t.Timestamp = time.Unix(ts, 0)
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *Store) QueryLogs(ctx context.Context, f LogFilter) ([]LogEntry, error) {
	query := `SELECT timestamp, container_id, container_name, project, service, stream, message FROM logs WHERE timestamp >= ? AND timestamp <= ?`
	args := []any{f.Start, f.End}

	// Service/project identity filter takes precedence over container ID filter.
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
		if err := rows.Scan(&ts, &e.ContainerID, &e.ContainerName, &e.Project, &e.Service, &e.Stream, &e.Message); err != nil {
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
func (s *Store) SaveTracking(ctx context.Context, containers, projects []string) error {
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
	for _, name := range projects {
		if _, err := stmt.ExecContext(ctx, "project", name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadTracking loads the persisted tracking state, split by kind.
func (s *Store) LoadTracking(ctx context.Context) (containers, projects []string, err error) {
	rows, err := s.db.QueryContext(ctx, "SELECT kind, name FROM tracking_state")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			return nil, nil, err
		}
		switch kind {
		case "container":
			containers = append(containers, name)
		case "project":
			projects = append(projects, name)
		}
	}
	return containers, projects, rows.Err()
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
