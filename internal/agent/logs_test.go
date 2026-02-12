package agent

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLogBatchInsert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	entries := []LogEntry{
		{Timestamp: time.Now(), ContainerID: "abc", ContainerName: "web", Stream: "stdout", Message: "hello"},
		{Timestamp: time.Now(), ContainerID: "abc", ContainerName: "web", Stream: "stderr", Message: "error"},
		{Timestamp: time.Now(), ContainerID: "def", ContainerName: "api", Stream: "stdout", Message: "started"},
	}

	if err := s.InsertLogs(context.Background(), entries); err != nil {
		t.Fatal(err)
	}

	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM logs").Scan(&count)
	if count != 3 {
		t.Errorf("log count = %d, want 3", count)
	}

	var msg string
	s.db.QueryRow("SELECT message FROM logs WHERE container_id = 'abc' AND stream = 'stdout'").Scan(&msg)
	if msg != "hello" {
		t.Errorf("message = %q, want hello", msg)
	}
}

func TestParseTimestamp(t *testing.T) {
	tests := []struct {
		line    string
		wantMsg string
		hasTS   bool
	}{
		{
			line:    "2024-01-15T10:30:00.123456789Z hello world",
			wantMsg: "hello world",
			hasTS:   true,
		},
		{
			line:    "just a plain log line",
			wantMsg: "just a plain log line",
			hasTS:   false,
		},
		{
			line:    "2024-01-15T10:30:00Z short timestamp",
			wantMsg: "short timestamp",
			hasTS:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.line[:20], func(t *testing.T) {
			ts, msg := parseTimestamp(tt.line)
			if msg != tt.wantMsg {
				t.Errorf("msg = %q, want %q", msg, tt.wantMsg)
			}
			if tt.hasTS && ts.Year() != 2024 {
				t.Errorf("year = %d, want 2024", ts.Year())
			}
			if !tt.hasTS && ts.Year() != time.Now().Year() {
				t.Errorf("expected current year for no-timestamp line, got %d", ts.Year())
			}
		})
	}
}

func TestSyncStartsAndStops(t *testing.T) {
	lt := &LogTailer{
		tailers: make(map[string]context.CancelFunc),
	}

	// Verify initial state.
	if len(lt.tailers) != 0 {
		t.Fatalf("initial tailers = %d, want 0", len(lt.tailers))
	}

	// We can't test actual tailing without Docker, but we can test
	// that Stop works on an empty tailer without hanging.
	lt.Stop()
}

// --- Chatty container characterization tests ---

func TestLogBatchFlushAtSize(t *testing.T) {
	// Characterization: scanLines sends every line to the channel with no rate
	// limiting. When accumulated entries reach logBatchSize, tail() flushes to
	// the store, which stores them all with no cap.
	s := testStore(t)
	lines := make(chan LogEntry, logBatchSize)
	r, w := io.Pipe()

	go func() {
		for i := 0; i < logBatchSize; i++ {
			fmt.Fprintf(w, "2024-01-15T10:30:00.000000000Z line %d\n", i)
		}
		w.Close()
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanLines(r, containerInfo{id: "chatty", name: "chatty"}, "stdout", lines)
	}()

	wg.Wait()
	close(lines)

	var batch []LogEntry
	for e := range lines {
		batch = append(batch, e)
	}

	if len(batch) != logBatchSize {
		t.Fatalf("scanLines produced %d entries, want %d", len(batch), logBatchSize)
	}

	// Flush the full batch to the store, matching production path.
	if err := s.InsertLogs(context.Background(), batch); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM logs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != logBatchSize {
		t.Errorf("store rows = %d, want %d — batch flushed in full with no cap", count, logBatchSize)
	}
}

func TestScannerLineLengthLimit(t *testing.T) {
	// Characterization: scanLines uses a 64KB buffer. A line exceeding this
	// causes the scanner to stop (bufio.ErrTooLong), silently dropping
	// all subsequent lines.
	lines := make(chan LogEntry, 10)

	longLine := strings.Repeat("x", 65*1024) // > 64KB
	input := "2024-01-15T10:30:00.000000000Z before\n" +
		longLine + "\n" +
		"2024-01-15T10:30:00.000000000Z after\n"

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanLines(strings.NewReader(input), containerInfo{id: "test", name: "test"}, "stdout", lines)
	}()

	wg.Wait()
	close(lines)

	var entries []LogEntry
	for e := range lines {
		entries = append(entries, e)
	}

	// Only the line before the oversized one is captured.
	// Scanner stops at the long line; everything after is lost.
	if len(entries) != 1 {
		t.Errorf("got %d entries, want 1 — scanner stops at oversized line", len(entries))
	}
	if len(entries) > 0 && entries[0].Message != "before" {
		t.Errorf("message = %q, want %q", entries[0].Message, "before")
	}
}
