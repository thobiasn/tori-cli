package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentLifecycle(t *testing.T) {
	if os.Getenv("ROOK_TEST_DOCKER") != "1" {
		t.Skip("set ROOK_TEST_DOCKER=1 to run Docker integration tests")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	cfg := &Config{
		Storage: StorageConfig{Path: dbPath, RetentionDays: 7},
		Host:    HostConfig{Proc: "/proc", Sys: "/sys"},
		Docker:  DockerConfig{Socket: "/var/run/docker.sock"},
		Collect: CollectConfig{Interval: Duration{Duration: 1 * time.Second}},
	}

	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Run for ~2 ticks then cancel.
	a.Run(ctx)

	// Verify data was written.
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var hostCount int
	store.db.QueryRow("SELECT COUNT(*) FROM host_metrics").Scan(&hostCount)
	if hostCount == 0 {
		t.Error("no host metrics found after running agent")
	}
}
