package agent

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

const (
	logBatchSize    = 100
	logFlushTimeout = 1 * time.Second
)

// LogTailer manages per-container log streaming goroutines.
type LogTailer struct {
	client  *client.Client
	store   *Store
	tailers map[string]context.CancelFunc // container ID -> cancel
	mu      sync.Mutex
	wg      sync.WaitGroup
	onEntry func(LogEntry) // called for each log entry, if set
}

// NewLogTailer creates a new log tailer.
func NewLogTailer(c *client.Client, store *Store) *LogTailer {
	return &LogTailer{
		client:  c,
		store:   store,
		tailers: make(map[string]context.CancelFunc),
	}
}

// Sync starts tailers for new containers and stops tailers for removed ones.
func (lt *LogTailer) Sync(ctx context.Context, containers []Container) {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	active := make(map[string]bool)
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		active[c.ID] = true

		if _, exists := lt.tailers[c.ID]; !exists {
			tailerCtx, cancel := context.WithCancel(ctx)
			lt.tailers[c.ID] = cancel
			lt.wg.Add(1)
			idProject, idService := serviceIdentity(c.Project, c.Service, c.Name)
			go lt.tail(tailerCtx, c.ID, c.Name, idProject, idService, c.StartedAt)
		}
	}

	// Stop tailers for containers that are no longer running.
	for id, cancel := range lt.tailers {
		if !active[id] {
			cancel()
			delete(lt.tailers, id)
		}
	}
}

// Stop cancels all tailers and waits for them to flush.
func (lt *LogTailer) Stop() {
	lt.mu.Lock()
	for id, cancel := range lt.tailers {
		cancel()
		delete(lt.tailers, id)
	}
	lt.mu.Unlock()

	lt.wg.Wait()
}

func (lt *LogTailer) tail(ctx context.Context, containerID, containerName, project, service string, startedAt int64) {
	defer lt.wg.Done()

	opts := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: true,
	}
	if startedAt > 0 {
		// Fetch logs from container start so we don't miss lines produced
		// between the container starting and the tailer attaching.
		opts.Since = time.Unix(startedAt, 0).UTC().Format(time.RFC3339)
	} else {
		opts.Tail = "0"
	}

	logs, err := lt.client.ContainerLogs(ctx, containerID, opts)
	if err != nil {
		slog.Warn("failed to start log tail", "container", containerName, "error", err)
		return
	}
	defer logs.Close()

	// Docker multiplexes stdout/stderr with 8-byte headers.
	// stdcopy.StdCopy demuxes into separate writers.
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	// Close the log stream when context is cancelled so the demux goroutine
	// unblocks even if the Docker client doesn't propagate cancellation.
	go func() {
		<-ctx.Done()
		logs.Close()
	}()

	// Demux in a goroutine since it blocks until the stream ends.
	go func() {
		defer stdoutW.Close()
		defer stderrW.Close()
		stdcopy.StdCopy(stdoutW, stderrW, logs)
	}()

	var batch []LogEntry
	flush := func() {
		if len(batch) == 0 {
			return
		}
		// Use background context for flush so it completes even after cancel.
		if err := lt.store.InsertLogs(context.Background(), batch); err != nil {
			slog.Warn("failed to insert logs", "container", containerName, "error", err)
		}
		batch = batch[:0]
	}

	// Read both stdout and stderr concurrently, merge into batch.
	lines := make(chan LogEntry, logBatchSize)
	var readerWg sync.WaitGroup
	readerWg.Add(2)

	ci := containerInfo{id: containerID, name: containerName, project: project, service: service}
	go func() {
		defer readerWg.Done()
		scanLines(stdoutR, ci, "stdout", lines)
	}()
	go func() {
		defer readerWg.Done()
		scanLines(stderrR, ci, "stderr", lines)
	}()

	go func() {
		readerWg.Wait()
		close(lines)
	}()

	timer := time.NewTimer(logFlushTimeout)
	defer timer.Stop()

	for {
		select {
		case entry, ok := <-lines:
			if !ok {
				flush()
				return
			}
			if lt.onEntry != nil {
				lt.onEntry(entry)
			}
			batch = append(batch, entry)
			if len(batch) >= logBatchSize {
				flush()
				timer.Reset(logFlushTimeout)
			}
		case <-timer.C:
			flush()
			timer.Reset(logFlushTimeout)
		}
	}
}

// containerInfo bundles the identity fields passed through log tailing.
type containerInfo struct {
	id, name, project, service string
}

func scanLines(r io.Reader, ci containerInfo, stream string, out chan<- LogEntry) {
	scanner := bufio.NewScanner(r)
	// Allow log lines up to 64KB.
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)

	for scanner.Scan() {
		line := scanner.Text()
		ts, msg := parseTimestamp(line)
		out <- LogEntry{
			Timestamp:     ts,
			ContainerID:   ci.id,
			ContainerName: ci.name,
			Project:       ci.project,
			Service:       ci.service,
			Stream:        stream,
			Message:       msg,
			Level:         InferLevel(msg),
			DisplayMsg:    ExtractDisplayMsg(msg),
		}
	}
}

// parseTimestamp extracts a Docker log timestamp prefix if present.
// Format: "2024-01-15T10:30:00.000000000Z message text"
func parseTimestamp(line string) (time.Time, string) {
	// Docker timestamps are RFC3339Nano, always 30+ chars before the space.
	if len(line) > 31 && line[4] == '-' && line[10] == 'T' {
		idx := 0
		for idx < len(line) && line[idx] != ' ' {
			idx++
		}
		if idx < len(line) {
			ts, err := time.Parse(time.RFC3339Nano, line[:idx])
			if err == nil {
				return ts, line[idx+1:]
			}
		}
	}
	return time.Now(), line
}
