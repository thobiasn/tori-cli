package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentLifecycle(t *testing.T) {
	if os.Getenv("TORI_TEST_DOCKER") != "1" {
		t.Skip("set TORI_TEST_DOCKER=1 to run Docker integration tests")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgPath, []byte(""), 0644)

	cfg := &Config{
		Storage: StorageConfig{Path: dbPath, RetentionDays: 7},
		Host:    HostConfig{Proc: "/proc", Sys: "/sys"},
		Docker:  DockerConfig{Socket: "/var/run/docker.sock"},
		Collect: CollectConfig{Interval: Duration{Duration: 1 * time.Second}},
	}

	a, err := New(cfg, cfgPath)
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

func TestReloadInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgPath, []byte(""), 0644)

	a := &Agent{
		cfgPath: cfgPath,
		reload:  make(chan *Config, 1),
	}

	// Valid reload should succeed.
	if err := a.Reload(); err != nil {
		t.Fatalf("valid reload failed: %v", err)
	}

	// Drain the channel.
	<-a.reload

	// Write invalid config.
	os.WriteFile(cfgPath, []byte("not valid [[[ toml"), 0644)

	if err := a.Reload(); err == nil {
		t.Fatal("expected error for invalid config")
	}

	// Channel should still be empty (no bad config sent).
	select {
	case <-a.reload:
		t.Fatal("bad config should not be queued")
	default:
	}
}

func TestReloadNonReloadableWarning(t *testing.T) {
	old := &Config{
		Storage: StorageConfig{Path: "/var/lib/tori/tori.db"},
		Socket:  SocketConfig{Path: "/run/tori/tori.sock"},
		Host:    HostConfig{Proc: "/proc", Sys: "/sys"},
		Docker:  DockerConfig{Socket: "/var/run/docker.sock"},
	}
	updated := &Config{
		Storage: StorageConfig{Path: "/tmp/other.db"},
		Socket:  SocketConfig{Path: "/tmp/other.sock"},
		Host:    HostConfig{Proc: "/host/proc", Sys: "/host/sys"},
		Docker:  DockerConfig{Socket: "/tmp/docker.sock"},
	}
	// This just verifies it doesn't panic; warnings go to slog.
	nonReloadableFields(old, updated)
}

func TestApplyConfigUpdatesFields(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	docker := &DockerCollector{
		include: []string{"web-*"},
		exclude: nil,
		prevCPU: make(map[string]cpuPrev),
		tracked: make(map[string]bool),
	}

	hub := NewHub()
	ew := &EventWatcher{
		docker: docker,
		hub:    hub,
		done:   make(chan struct{}),
	}
	ss := NewSocketServer(hub, store, docker, nil, 7)

	a := &Agent{
		cfg: &Config{
			Storage: StorageConfig{Path: dbPath, RetentionDays: 7},
			Collect: CollectConfig{Interval: Duration{Duration: 10 * time.Second}},
			Docker:  DockerConfig{Include: []string{"web-*"}},
		},
		store:  store,
		docker: docker,
		hub:    hub,
		events: ew,
		socket: ss,
		reload: make(chan *Config, 1),
	}

	newCfg := &Config{
		Storage: StorageConfig{Path: dbPath, RetentionDays: 14},
		Collect: CollectConfig{Interval: Duration{Duration: 30 * time.Second}},
		Docker:  DockerConfig{Include: []string{"api-*"}, Exclude: []string{"test-*"}},
	}

	a.applyConfig(newCfg)

	if a.cfg.Storage.RetentionDays != 14 {
		t.Errorf("retention = %d, want 14", a.cfg.Storage.RetentionDays)
	}
	if got := ss.retentionDays.Load(); got != 14 {
		t.Errorf("socket retentionDays = %d, want 14", got)
	}
	if a.cfg.Collect.Interval.Duration != 30*time.Second {
		t.Errorf("interval = %s, want 30s", a.cfg.Collect.Interval.Duration)
	}
	// Verify filters were updated.
	if !docker.MatchFilter("api-test") {
		t.Error("api-test should match new include pattern")
	}
	if docker.MatchFilter("web-app") {
		t.Error("web-app should no longer match after filter update")
	}
	if docker.MatchFilter("test-runner") {
		t.Error("test-runner should be excluded")
	}
}

func TestApplyConfigRebuildsAlerter(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	docker := &DockerCollector{
		prevCPU: make(map[string]cpuPrev),
		tracked: make(map[string]bool),
	}

	hub := NewHub()
	ew := &EventWatcher{docker: docker, hub: hub, done: make(chan struct{})}
	ss := NewSocketServer(hub, store, docker, nil, 7)

	a := &Agent{
		cfg: &Config{
			Storage: StorageConfig{Path: dbPath, RetentionDays: 7},
			Collect: CollectConfig{Interval: Duration{Duration: 10 * time.Second}},
		},
		store:  store,
		docker: docker,
		hub:    hub,
		events: ew,
		socket: ss,
		reload: make(chan *Config, 1),
	}

	// Initially no alerter.
	if a.alerter != nil {
		t.Fatal("alerter should be nil initially")
	}

	// Reload with alert rules.
	newCfg := &Config{
		Storage: StorageConfig{Path: dbPath, RetentionDays: 7},
		Collect: CollectConfig{Interval: Duration{Duration: 10 * time.Second}},
		Alerts: map[string]AlertConfig{
			"high_cpu": {
				Condition: "host.cpu_percent > 90",
				Severity:  "critical",
				Actions:   []string{"notify"},
			},
		},
	}

	a.applyConfig(newCfg)

	if a.alerter == nil {
		t.Fatal("alerter should be set after reload with alert rules")
	}
	if !a.alerter.HasRule("high_cpu") {
		t.Error("alerter should have high_cpu rule")
	}

	// Reload without alert rules should clear alerter.
	noAlertCfg := &Config{
		Storage: StorageConfig{Path: dbPath, RetentionDays: 7},
		Collect: CollectConfig{Interval: Duration{Duration: 10 * time.Second}},
	}

	a.applyConfig(noAlertCfg)

	if a.alerter != nil {
		t.Error("alerter should be nil after reload without alert rules")
	}
}
