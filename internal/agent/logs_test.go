package agent

import (
	"context"
	"path/filepath"
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
