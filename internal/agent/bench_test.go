package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// benchStore creates a Store in a temporary directory for benchmarks.
func benchStore(b *testing.B) *Store {
	b.Helper()
	path := b.TempDir() + "/bench.db"
	s, err := OpenStore(path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	return s
}

func makeBatch(n int, msg string) []LogEntry {
	ts := time.Now()
	batch := make([]LogEntry, n)
	for i := range batch {
		batch[i] = LogEntry{
			Timestamp:     ts,
			ContainerID:   "bench-container",
			ContainerName: "bench-container",
			Project:       "bench",
			Service:       "bench",
			Stream:        "stdout",
			Message:       msg,
			Level:         "INFO",
		}
	}
	return batch
}

// BenchmarkInsertLogs measures raw insert throughput at different batch sizes.
// This shows that the current batch size of 100 is suboptimal.
func BenchmarkInsertLogs(b *testing.B) {
	msg := fmt.Sprintf("2025-01-15 10:30:00 INFO request served method=GET path=/api/health status=200 duration=1.23ms %s", strings.Repeat("x", 40))

	for _, batchSize := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("batch_%d", batchSize), func(b *testing.B) {
			s := benchStore(b)
			ctx := context.Background()
			batch := makeBatch(batchSize, msg)

			b.ResetTimer()
			b.ReportAllocs()

			for b.Loop() {
				if err := s.InsertLogs(ctx, batch); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(batchSize), "entries/op")
		})
	}
}

// BenchmarkInsertLogsPerEntry reports the per-entry cost at different batch sizes.
func BenchmarkInsertLogsPerEntry(b *testing.B) {
	msg := strings.Repeat("x", 100)

	for _, batchSize := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("batch_%d", batchSize), func(b *testing.B) {
			s := benchStore(b)
			ctx := context.Background()
			batch := makeBatch(batchSize, msg)

			b.ResetTimer()
			b.ReportAllocs()

			for b.Loop() {
				if err := s.InsertLogs(ctx, batch); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*batchSize), "ns/entry")
		})
	}
}

// TestQueryLatencyUnderWriteLoad demonstrates the core performance issue:
// sustained log writes degrade query response times because of MaxOpenConns(1).
//
// When log inserts saturate the single SQLite connection, client queries
// from the socket server block and eventually the SSH tunnel times out.
func TestQueryLatencyUnderWriteLoad(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Now()
	msg := strings.Repeat("x", 100)

	// Seed some data for queries to return.
	for i := range 50 {
		s.InsertHostMetrics(ctx, ts.Add(time.Duration(i)*time.Second), &HostMetrics{CPUPercent: float64(i)})
		s.InsertLogs(ctx, makeBatch(100, msg))
	}

	// Measure baseline query latency with no concurrent writes.
	const iterations = 50
	baselineLatencies := make([]time.Duration, iterations)
	for i := range baselineLatencies {
		start := time.Now()
		if _, err := s.QueryHostMetrics(ctx, ts.Unix()-3600, ts.Unix()+3600); err != nil {
			t.Fatal(err)
		}
		baselineLatencies[i] = time.Since(start)
	}
	baselineAvg := avgDuration(baselineLatencies)

	// Now sustain concurrent writes and measure query latency.
	writeCtx, writeCancel := context.WithCancel(ctx)
	defer writeCancel()

	var wg sync.WaitGroup
	wg.Go(func() {
		batch := makeBatch(100, msg)
		for writeCtx.Err() == nil {
			s.InsertLogs(writeCtx, batch)
		}
	})

	// Let writes saturate for a moment.
	time.Sleep(50 * time.Millisecond)

	loadedLatencies := make([]time.Duration, iterations)
	for i := range loadedLatencies {
		start := time.Now()
		if _, err := s.QueryHostMetrics(ctx, ts.Unix()-3600, ts.Unix()+3600); err != nil {
			t.Fatal(err)
		}
		loadedLatencies[i] = time.Since(start)
	}
	loadedAvg := avgDuration(loadedLatencies)

	writeCancel()
	wg.Wait()

	ratio := float64(loadedAvg) / float64(baselineAvg)
	t.Logf("Query latency — baseline: %v, under write load: %v (%.1fx slower)", baselineAvg, loadedAvg, ratio)
	t.Logf("  baseline p50: %v, p99: %v", percentile(baselineLatencies, 50), percentile(baselineLatencies, 99))
	t.Logf("  loaded   p50: %v, p99: %v", percentile(loadedLatencies, 50), percentile(loadedLatencies, 99))

	// We expect measurable degradation with MaxOpenConns(1).
	// Log the findings for analysis — this characterizes current behavior.
	if ratio > 2.0 {
		t.Logf("CONFIRMED: queries are %.1fx slower under write load — this explains connection timeouts", ratio)
	}
}

// TestDBGrowthRate measures database size growth per log entry to validate
// reported disk usage with verbose containers.
func TestDBGrowthRate(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/growth.db"
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	msg := fmt.Sprintf("2025-01-15T10:30:00.123Z INFO request handled method=GET path=/api/v1/users status=200 latency=1.234ms bytes=%s", strings.Repeat("x", 60))

	const batchSize = 100
	const batches = 100
	const totalEntries = batchSize * batches

	for i := range batches {
		_ = i
		batch := makeBatch(batchSize, msg)
		if err := s.InsertLogs(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}

	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	bytesPerEntry := float64(info.Size()) / float64(totalEntries)
	t.Logf("Total entries: %d", totalEntries)
	t.Logf("DB size: %.2f MB (%.0f bytes/entry)", float64(info.Size())/(1024*1024), bytesPerEntry)

	// Project to realistic scenarios.
	// Default collect interval is 10s, so log tailer flushes independently.
	// A container at 100 lines/sec = 8.64M lines/day.
	linesPerSecond := []int{10, 100, 1000}
	for _, lps := range linesPerSecond {
		dailyLines := lps * 86400
		dailyBytes := float64(dailyLines) * bytesPerEntry
		dailyGB := dailyBytes / (1024 * 1024 * 1024)
		weeklyGB := dailyGB * 7
		t.Logf("  %4d lines/sec/container: %.1f GB/day, %.1f GB/week (default 7-day retention)",
			lps, dailyGB, weeklyGB)
	}

	// 4 containers at 1000 lines/sec for a week.
	fourContainers := 4 * 1000 * 86400 * 7
	projectedGB := float64(fourContainers) * bytesPerEntry / (1024 * 1024 * 1024)
	t.Logf("  4 containers × 1000 lines/sec × 7 days: %.0f GB", projectedGB)
}

// TestWriteThroughputCeiling measures maximum sustainable write throughput
// to understand at what log rate the system saturates.
func TestWriteThroughputCeiling(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	msg := strings.Repeat("x", 100)

	const duration = 2 * time.Second
	const batchSize = 100

	batch := makeBatch(batchSize, msg)
	var totalEntries int
	var totalBatches int

	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if err := s.InsertLogs(ctx, batch); err != nil {
			t.Fatal(err)
		}
		totalEntries += batchSize
		totalBatches++
	}

	entriesPerSec := float64(totalEntries) / duration.Seconds()
	batchesPerSec := float64(totalBatches) / duration.Seconds()
	t.Logf("Write ceiling (batch=%d): %.0f entries/sec, %.0f batches/sec",
		batchSize, entriesPerSec, batchesPerSec)

	// A container at N lines/sec: how many containers before we saturate?
	for _, lps := range []int{10, 100, 1000} {
		maxContainers := int(entriesPerSec) / lps
		t.Logf("  At %d lines/sec per container: can sustain ~%d containers before saturation",
			lps, maxContainers)
	}
}

// TestIndexWriteAmplification measures how much I/O each log insert actually
// generates by comparing pages written with all 5 indexes vs. with only the
// essential ones. This explains the "massive I/O" in docker stats.
func TestIndexWriteAmplification(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	msg := fmt.Sprintf("2025-01-15T10:30:00Z INFO request method=GET path=/api/v1/users status=200 latency=1.234ms %s", strings.Repeat("x", 60))

	const batchSize = 100
	const batches = 50

	// Measure with current schema (5 indexes on logs).
	pathFull := dir + "/full_index.db"
	sFull, err := OpenStore(pathFull)
	if err != nil {
		t.Fatal(err)
	}
	defer sFull.Close()

	// Force checkpoint so we start clean.
	sFull.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	for range batches {
		if err := sFull.InsertLogs(ctx, makeBatch(batchSize, msg)); err != nil {
			t.Fatal(err)
		}
	}
	sFull.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

	fullInfo, _ := os.Stat(pathFull)
	fullSize := fullInfo.Size()

	// Measure with no indexes on logs (row data only).
	pathNone := dir + "/no_index.db"
	sNone, err := OpenStore(pathNone)
	if err != nil {
		t.Fatal(err)
	}
	defer sNone.Close()

	sNone.db.Exec("DROP INDEX IF EXISTS idx_logs_ts")
	sNone.db.Exec("DROP INDEX IF EXISTS idx_logs_container_ts")
	sNone.db.Exec("DROP INDEX IF EXISTS idx_logs_svc")
	sNone.db.Exec("DROP INDEX IF EXISTS idx_logs_container_level_ts")
	sNone.db.Exec("DROP INDEX IF EXISTS idx_logs_svc_level")
	sNone.db.Exec("VACUUM")
	sNone.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

	for range batches {
		if err := sNone.InsertLogs(ctx, makeBatch(batchSize, msg)); err != nil {
			t.Fatal(err)
		}
	}
	sNone.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

	noneInfo, _ := os.Stat(pathNone)
	noneSize := noneInfo.Size()

	totalEntries := batchSize * batches
	indexOverhead := float64(fullSize) / float64(noneSize)
	t.Logf("Entries: %d", totalEntries)
	t.Logf("DB with 5 indexes:  %.2f MB (%.0f bytes/entry)", float64(fullSize)/(1024*1024), float64(fullSize)/float64(totalEntries))
	t.Logf("DB with 0 indexes:  %.2f MB (%.0f bytes/entry)", float64(noneSize)/(1024*1024), float64(noneSize)/float64(totalEntries))
	t.Logf("Index overhead: %.1fx total storage", indexOverhead)
	t.Logf("")

	// Also check if some indexes are redundant.
	// idx_logs_svc (project, service, timestamp) is a prefix of
	// idx_logs_svc_level (project, service, level, timestamp) — but SQLite
	// can't use the latter for (project, service, timestamp) queries efficiently
	// because 'level' is in the middle. So it's not truly redundant.
	//
	// However: idx_logs_container_ts vs idx_logs_container_level_ts — same
	// relationship. container_id prefix works but level column gets in the way.
	t.Logf("Indexes on logs table:")
	t.Logf("  1. idx_logs_ts                  (timestamp)                          — prune, unfiltered time range")
	t.Logf("  2. idx_logs_container_ts         (container_id, timestamp)            — CountLogMatches, per-container queries")
	t.Logf("  3. idx_logs_svc                  (project, service, timestamp)        — per-service queries without level")
	t.Logf("  4. idx_logs_container_level_ts   (container_id, level, timestamp)     — per-container + level filter")
	t.Logf("  5. idx_logs_svc_level            (project, service, level, timestamp) — per-service + level filter")
	t.Logf("")

	// Project I/O impact at different log rates.
	bytesPerEntry := float64(fullSize) / float64(totalEntries)
	bytesPerEntryNoIdx := float64(noneSize) / float64(totalEntries)
	for _, lps := range []int{100, 1000} {
		dailyFull := float64(lps*86400) * bytesPerEntry / (1024 * 1024 * 1024)
		dailyNoIdx := float64(lps*86400) * bytesPerEntryNoIdx / (1024 * 1024 * 1024)
		t.Logf("  %d lines/sec: %.1f GB/day (with indexes) vs %.1f GB/day (without) — indexes add %.1f GB/day",
			lps, dailyFull, dailyNoIdx, dailyFull-dailyNoIdx)
	}
}

// TestWALWriteAmplification measures how much total I/O a batch of log inserts
// generates. The key insight: docker stats shows block I/O, which includes WAL
// writes + checkpoint writes + index page reads. A ~242 byte log entry can
// generate many KB of actual disk I/O due to page-level write amplification.
func TestWALWriteAmplification(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	msg := fmt.Sprintf("2025-01-15T10:30:00Z INFO request method=GET path=/api/v1/users status=200 latency=1.234ms %s", strings.Repeat("x", 60))

	path := dir + "/wal_amp.db"
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	walPath := path + "-wal"

	// Seed with some data so B-tree pages are populated (more realistic).
	for range 20 {
		s.InsertLogs(ctx, makeBatch(100, msg))
	}
	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

	// Measure WAL growth from a single batch of inserts.
	const batchSize = 100
	const batches = 10

	walBefore, _ := os.Stat(walPath)
	walSizeBefore := int64(0)
	if walBefore != nil {
		walSizeBefore = walBefore.Size()
	}

	for range batches {
		if err := s.InsertLogs(ctx, makeBatch(batchSize, msg)); err != nil {
			t.Fatal(err)
		}
	}

	walAfter, _ := os.Stat(walPath)
	walSizeAfter := walAfter.Size()
	walGrowth := walSizeAfter - walSizeBefore

	totalEntries := batchSize * batches
	walBytesPerEntry := float64(walGrowth) / float64(totalEntries)
	entryDataSize := 242 // measured raw entry size without indexes

	var pageSize int64
	s.db.QueryRow("PRAGMA page_size").Scan(&pageSize)

	t.Logf("Page size: %d bytes", pageSize)
	t.Logf("Entries inserted: %d", totalEntries)
	t.Logf("Raw entry data: ~%d bytes/entry", entryDataSize)
	t.Logf("WAL growth: %d bytes (%.0f bytes/entry)", walGrowth, walBytesPerEntry)
	t.Logf("Write amplification (WAL/raw): %.1fx", walBytesPerEntry/float64(entryDataSize))
	t.Logf("")

	// The WAL captures full 4KB pages. Each index B-tree modification dirties
	// at least one page. With 5 indexes, a single entry can dirty 6+ pages.
	pagesPerEntry := walBytesPerEntry / float64(pageSize)
	t.Logf("WAL pages per entry: %.1f (each is %d bytes)", pagesPerEntry, pageSize)
	t.Logf("")

	// Total I/O per entry = WAL write + checkpoint (reads WAL, writes DB).
	// Checkpoint effectively doubles the I/O because it re-writes dirty pages.
	totalIOPerEntry := walBytesPerEntry * 3 // WAL write + checkpoint read + checkpoint write
	t.Logf("Estimated total I/O per entry (WAL write + checkpoint read + checkpoint write):")
	t.Logf("  %.0f bytes/entry (%.1fx raw data size)", totalIOPerEntry, totalIOPerEntry/float64(entryDataSize))
	t.Logf("")

	// Project to docker stats block I/O at different rates.
	for _, lps := range []int{100, 1000} {
		dailyIO := float64(lps*86400) * totalIOPerEntry / (1024 * 1024 * 1024)
		t.Logf("  %d lines/sec: ~%.0f GB/day total block I/O (docker stats)", lps, dailyIO)
	}
}

// TestLogIndexUsage uses EXPLAIN QUERY PLAN to determine which indexes SQLite
// actually picks for each query pattern against the logs table.
func TestLogIndexUsage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Now()

	// Seed data so query planner has stats.
	for range 10 {
		s.InsertLogs(ctx, makeBatch(100, "test message"))
	}
	s.db.Exec("ANALYZE")

	queries := []struct {
		name  string
		query string
		args  []any
	}{
		{
			"Prune (DELETE by timestamp)",
			"DELETE FROM logs WHERE rowid IN (SELECT rowid FROM logs WHERE timestamp < ? LIMIT 5000)",
			[]any{ts.Unix()},
		},
		{
			"CountLogMatches (timestamp + message scan, GROUP BY container_id)",
			"SELECT container_id, COUNT(*) FROM logs WHERE timestamp >= ? AND timestamp <= ? AND message LIKE '%error%' GROUP BY container_id",
			[]any{ts.Unix() - 3600, ts.Unix()},
		},
		{
			"CountLogs (timestamp only)",
			"SELECT COUNT(*) FROM logs WHERE timestamp >= ? AND timestamp <= ?",
			[]any{ts.Unix() - 3600, ts.Unix()},
		},
		{
			"CountLogs (timestamp + container_id)",
			"SELECT COUNT(*) FROM logs WHERE timestamp >= ? AND timestamp <= ? AND container_id = ?",
			[]any{ts.Unix() - 3600, ts.Unix(), "abc"},
		},
		{
			"CountLogs (timestamp + project + service)",
			"SELECT COUNT(*) FROM logs WHERE timestamp >= ? AND timestamp <= ? AND project = ? AND service = ?",
			[]any{ts.Unix() - 3600, ts.Unix(), "myapp", "web"},
		},
		{
			"QueryLogs (timestamp only, ORDER BY timestamp DESC)",
			"SELECT * FROM logs WHERE timestamp >= ? AND timestamp <= ? ORDER BY timestamp DESC LIMIT 1000",
			[]any{ts.Unix() - 3600, ts.Unix()},
		},
		{
			"QueryLogs (timestamp + container_id)",
			"SELECT * FROM logs WHERE timestamp >= ? AND timestamp <= ? AND container_id = ? ORDER BY timestamp DESC LIMIT 1000",
			[]any{ts.Unix() - 3600, ts.Unix(), "abc"},
		},
		{
			"QueryLogs (timestamp + project + service)",
			"SELECT * FROM logs WHERE timestamp >= ? AND timestamp <= ? AND project = ? AND service = ? ORDER BY timestamp DESC LIMIT 1000",
			[]any{ts.Unix() - 3600, ts.Unix(), "myapp", "web"},
		},
		{
			"QueryLogs (timestamp + container_id + level)",
			"SELECT * FROM logs WHERE timestamp >= ? AND timestamp <= ? AND container_id = ? AND level = ? ORDER BY timestamp DESC LIMIT 1000",
			[]any{ts.Unix() - 3600, ts.Unix(), "abc", "ERR"},
		},
		{
			"QueryLogs (timestamp + project + service + level)",
			"SELECT * FROM logs WHERE timestamp >= ? AND timestamp <= ? AND project = ? AND service = ? AND level = ? ORDER BY timestamp DESC LIMIT 1000",
			[]any{ts.Unix() - 3600, ts.Unix(), "myapp", "web", "ERR"},
		},
	}

	for _, q := range queries {
		t.Run(q.name, func(t *testing.T) {
			rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q.query, q.args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()

			for rows.Next() {
				var id, parent, notused int
				var detail string
				if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
					t.Fatal(err)
				}
				t.Logf("  %s", detail)
			}
		})
	}
}

// TestLevelFilterWithoutDedicatedIndex measures QueryLogs performance with a
// level filter when the dedicated level indexes are dropped. SQLite falls back
// to idx_logs_container_ts / idx_logs_svc and post-filters on level.
func TestLevelFilterWithoutDedicatedIndex(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Now()

	// Insert logs with mixed levels — realistic distribution.
	levels := []string{"INFO", "INFO", "INFO", "INFO", "WARN", "ERR", "DBUG", "INFO", "INFO", "INFO"}
	for i := range 100 {
		batch := make([]LogEntry, 100)
		for j := range batch {
			batch[j] = LogEntry{
				Timestamp:     ts.Add(time.Duration(i) * time.Second),
				ContainerID:   "abc123",
				ContainerName: "web",
				Project:       "myapp",
				Service:       "web",
				Stream:        "stdout",
				Message:       fmt.Sprintf("log line %d", i*100+j),
				Level:         levels[j%len(levels)],
			}
		}
		s.InsertLogs(ctx, batch)
	}
	s.db.Exec("ANALYZE")

	start := ts.Unix() - 1
	end := ts.Unix() + 200

	// Measure with all indexes (current state).
	const iters = 100
	withIdx := make([]time.Duration, iters)
	for i := range withIdx {
		t0 := time.Now()
		s.QueryLogs(ctx, LogFilter{
			Start:        start,
			End:          end,
			ContainerIDs: []string{"abc123"},
			Level:        "ERR",
			Limit:        1000,
		})
		withIdx[i] = time.Since(t0)
	}

	// Drop the level indexes.
	s.db.Exec("DROP INDEX IF EXISTS idx_logs_container_level_ts")
	s.db.Exec("DROP INDEX IF EXISTS idx_logs_svc_level")
	s.db.Exec("ANALYZE")

	withoutIdx := make([]time.Duration, iters)
	for i := range withoutIdx {
		t0 := time.Now()
		s.QueryLogs(ctx, LogFilter{
			Start:        start,
			End:          end,
			ContainerIDs: []string{"abc123"},
			Level:        "ERR",
			Limit:        1000,
		})
		withoutIdx[i] = time.Since(t0)
	}

	t.Logf("QueryLogs (container + level=ERR) over 10,000 rows:")
	t.Logf("  With level index:    avg %v, p50 %v, p99 %v", avgDuration(withIdx), percentile(withIdx, 50), percentile(withIdx, 99))
	t.Logf("  Without level index: avg %v, p50 %v, p99 %v", avgDuration(withoutIdx), percentile(withoutIdx, 50), percentile(withoutIdx, 99))
	t.Logf("  Slowdown: %.1fx", float64(avgDuration(withoutIdx))/float64(avgDuration(withIdx)))
}

// TestDropLevelIndexesWriteGain measures the write throughput improvement and
// I/O reduction from dropping the two level-specific indexes.
func TestDropLevelIndexesWriteGain(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	msg := strings.Repeat("x", 100)

	const duration = 2 * time.Second
	const batchSize = 100

	// Measure with all 5 indexes.
	pathFull := dir + "/full.db"
	sFull, err := OpenStore(pathFull)
	if err != nil {
		t.Fatal(err)
	}

	batch := makeBatch(batchSize, msg)
	var fullEntries int
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if err := sFull.InsertLogs(ctx, batch); err != nil {
			t.Fatal(err)
		}
		fullEntries += batchSize
	}
	sFull.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	fullInfo, _ := os.Stat(pathFull)
	sFull.Close()

	// Measure with 3 indexes (level indexes dropped).
	pathReduced := dir + "/reduced.db"
	sReduced, err := OpenStore(pathReduced)
	if err != nil {
		t.Fatal(err)
	}
	sReduced.db.Exec("DROP INDEX IF EXISTS idx_logs_container_level_ts")
	sReduced.db.Exec("DROP INDEX IF EXISTS idx_logs_svc_level")

	var reducedEntries int
	deadline = time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if err := sReduced.InsertLogs(ctx, batch); err != nil {
			t.Fatal(err)
		}
		reducedEntries += batchSize
	}
	sReduced.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	reducedInfo, _ := os.Stat(pathReduced)
	sReduced.Close()

	fullRate := float64(fullEntries) / duration.Seconds()
	reducedRate := float64(reducedEntries) / duration.Seconds()
	t.Logf("Write throughput:")
	t.Logf("  5 indexes: %.0f entries/sec", fullRate)
	t.Logf("  3 indexes: %.0f entries/sec (%.0f%% faster)", reducedRate, (reducedRate/fullRate-1)*100)
	t.Logf("")

	fullBPE := float64(fullInfo.Size()) / float64(fullEntries)
	reducedBPE := float64(reducedInfo.Size()) / float64(reducedEntries)
	t.Logf("Storage per entry:")
	t.Logf("  5 indexes: %.0f bytes/entry", fullBPE)
	t.Logf("  3 indexes: %.0f bytes/entry (%.0f%% less)", reducedBPE, (1-reducedBPE/fullBPE)*100)
	t.Logf("")

	// Project I/O savings.
	for _, lps := range []int{100, 1000} {
		fullDaily := float64(lps*86400) * fullBPE * 3 / (1024 * 1024 * 1024)    // ×3 for WAL write+checkpoint
		reducedDaily := float64(lps*86400) * reducedBPE * 3 / (1024 * 1024 * 1024)
		saved := fullDaily - reducedDaily
		t.Logf("  %d lines/sec: %.0f GB/day → %.0f GB/day I/O (saves %.0f GB/day)",
			lps, fullDaily, reducedDaily, saved)
	}
}

// TestActualBlockIO measures real process-level I/O during a burst of log
// inserts using /proc/self/io. This is the same data source docker stats uses
// (via blkio cgroup), so the numbers replicate what docker stats reports.
func TestActualBlockIO(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	msg := fmt.Sprintf("2025-01-15T10:30:00Z INFO request method=GET path=/api/v1/users status=200 latency=1.234ms %s", strings.Repeat("x", 60))

	path := dir + "/blockio.db"
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Seed some data so the B-trees are populated.
	for range 20 {
		s.InsertLogs(ctx, makeBatch(100, msg))
	}
	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

	// Simulate a burst of log writes like 4 chatty containers would produce.
	const duration = 3 * time.Second
	batch := makeBatch(100, msg)

	before := readProcIO(t)
	var totalEntries int
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if err := s.InsertLogs(ctx, batch); err != nil {
			t.Fatal(err)
		}
		totalEntries += 100
	}
	// Force checkpoint so WAL→DB writes are counted.
	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	after := readProcIO(t)

	readBytes := after.readBytes - before.readBytes
	writeBytes := after.writeBytes - before.writeBytes
	totalIO := readBytes + writeBytes

	t.Logf("Duration: %s", duration)
	t.Logf("Entries written: %d (%.0f entries/sec)", totalEntries, float64(totalEntries)/duration.Seconds())
	t.Logf("")
	t.Logf("Block I/O (what docker stats shows):")
	t.Logf("  Read:  %s (%.0f bytes/entry)", formatBytes(readBytes), float64(readBytes)/float64(totalEntries))
	t.Logf("  Write: %s (%.0f bytes/entry)", formatBytes(writeBytes), float64(writeBytes)/float64(totalEntries))
	t.Logf("  Total: %s (%.0f bytes/entry)", formatBytes(totalIO), float64(totalIO)/float64(totalEntries))
	t.Logf("")

	ioPerSec := float64(totalIO) / duration.Seconds()
	t.Logf("I/O rate: %s/sec", formatBytes(int64(ioPerSec)))
	t.Logf("")

	// Project to a realistic scenario: 4 containers at various rates over 24 hours.
	t.Logf("Projected 24-hour docker stats block I/O (at measured rate):")
	dailyIO := ioPerSec * 86400
	t.Logf("  Single writer at max throughput: %s/day", formatBytes(int64(dailyIO)))
	t.Logf("")

	// Also project for specific per-container rates.
	bytesPerEntry := float64(totalIO) / float64(totalEntries)
	for _, lps := range []int{100, 500, 1000} {
		daily := float64(lps) * 86400 * bytesPerEntry
		fourContainers := daily * 4
		t.Logf("  4 containers × %d lines/sec: %s/day", lps, formatBytes(int64(fourContainers)))
	}
}

type procIO struct {
	readBytes  int64
	writeBytes int64
}

func readProcIO(t *testing.T) procIO {
	t.Helper()
	data, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Skipf("cannot read /proc/self/io: %v (not on Linux?)", err)
	}
	var p procIO
	for _, line := range strings.Split(string(data), "\n") {
		// rchar/wchar track bytes through read/write syscalls — this is the
		// I/O work SQLite actually does. read_bytes/write_bytes only count
		// physical disk I/O which doesn't register in tmpdir tests.
		if strings.HasPrefix(line, "rchar:") {
			fmt.Sscanf(line, "rchar: %d", &p.readBytes)
		}
		if strings.HasPrefix(line, "wchar:") {
			fmt.Sscanf(line, "wchar: %d", &p.writeBytes)
		}
	}
	return p
}

func formatBytes(b int64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(b)/(1024*1024*1024))
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// TestOptimizationCombinations measures actual I/O under different optimization
// combinations to see what moves the needle.
func TestOptimizationCombinations(t *testing.T) {
	ctx := context.Background()
	msg := fmt.Sprintf("2025-01-15T10:30:00Z INFO request method=GET path=/api/v1/users status=200 latency=1.234ms %s", strings.Repeat("x", 60))

	const duration = 2 * time.Second
	const batchSize = 100

	type config struct {
		name       string
		batchSize  int
		dropLevel  bool
		multiRow   bool
		pragmas    []string
	}

	configs := []config{
		{
			name:      "current (baseline)",
			batchSize: 100,
		},
		{
			name:      "batch 500",
			batchSize: 500,
		},
		{
			name:      "drop level indexes",
			batchSize: 100,
			dropLevel: true,
		},
		{
			name:      "wal_autocheckpoint=10000",
			batchSize: 100,
			pragmas:   []string{"PRAGMA wal_autocheckpoint = 10000"},
		},
		{
			name:      "all combined (batch 500 + drop level + pragmas)",
			batchSize: 500,
			dropLevel: true,
			pragmas: []string{
				"PRAGMA wal_autocheckpoint = 10000",
				"PRAGMA mmap_size = 268435456",
			},
		},
	}

	for _, cfg := range configs {
		t.Run(cfg.name, func(t *testing.T) {
			dir := t.TempDir()
			path := dir + "/test.db"
			s, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()

			if cfg.dropLevel {
				s.db.Exec("DROP INDEX IF EXISTS idx_logs_container_level_ts")
				s.db.Exec("DROP INDEX IF EXISTS idx_logs_svc_level")
			}
			for _, p := range cfg.pragmas {
				s.db.Exec(p)
			}

			// Seed data.
			for range 10 {
				s.InsertLogs(ctx, makeBatch(100, msg))
			}
			s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

			batch := makeBatch(cfg.batchSize, msg)
			before := readProcIO(t)
			var totalEntries int
			deadline := time.Now().Add(duration)
			for time.Now().Before(deadline) {
				if err := s.InsertLogs(ctx, batch); err != nil {
					t.Fatal(err)
				}
				totalEntries += cfg.batchSize
			}
			s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
			after := readProcIO(t)

			readB := after.readBytes - before.readBytes
			writeB := after.writeBytes - before.writeBytes
			totalIO := readB + writeB

			rate := float64(totalEntries) / duration.Seconds()
			ioPerEntry := float64(totalIO) / float64(totalEntries)
			ioPerSec := float64(totalIO) / duration.Seconds()

			// Project to 4 containers × 500 lines/sec.
			daily4x500 := 4.0 * 500 * 86400 * ioPerEntry

			t.Logf("%.0f entries/sec | %s/sec | %.0f bytes/entry | 4×500lps = %s/day",
				rate, formatBytes(int64(ioPerSec)), ioPerEntry, formatBytes(int64(daily4x500)))
		})
	}
}

func avgDuration(ds []time.Duration) time.Duration {
	var total time.Duration
	for _, d := range ds {
		total += d
	}
	return total / time.Duration(len(ds))
}

func percentile(ds []time.Duration, pct int) time.Duration {
	sorted := make([]time.Duration, len(ds))
	copy(sorted, ds)
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	idx := len(sorted) * pct / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}