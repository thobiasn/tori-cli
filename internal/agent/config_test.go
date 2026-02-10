package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigFull(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[storage]
path = "/tmp/test.db"
retention_days = 14

[socket]
path = "/tmp/test.sock"

[host]
proc = "/host/proc"
sys = "/host/sys"

[docker]
socket = "/var/run/docker.sock"
include = ["web-*"]
exclude = ["test-*"]

[collect]
interval = "30s"
`), 0644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Storage.Path != "/tmp/test.db" {
		t.Errorf("storage path = %q, want /tmp/test.db", cfg.Storage.Path)
	}
	if cfg.Storage.RetentionDays != 14 {
		t.Errorf("retention_days = %d, want 14", cfg.Storage.RetentionDays)
	}
	if cfg.Socket.Path != "/tmp/test.sock" {
		t.Errorf("socket path = %q, want /tmp/test.sock", cfg.Socket.Path)
	}
	if cfg.Host.Proc != "/host/proc" {
		t.Errorf("proc = %q, want /host/proc", cfg.Host.Proc)
	}
	if cfg.Host.Sys != "/host/sys" {
		t.Errorf("sys = %q, want /host/sys", cfg.Host.Sys)
	}
	if len(cfg.Docker.Include) != 1 || cfg.Docker.Include[0] != "web-*" {
		t.Errorf("docker include = %v, want [web-*]", cfg.Docker.Include)
	}
	if len(cfg.Docker.Exclude) != 1 || cfg.Docker.Exclude[0] != "test-*" {
		t.Errorf("docker exclude = %v, want [test-*]", cfg.Docker.Exclude)
	}
	if cfg.Collect.Interval.Duration != 30*time.Second {
		t.Errorf("interval = %s, want 30s", cfg.Collect.Interval.Duration)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(""), 0644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Storage.Path != "/var/lib/rook/rook.db" {
		t.Errorf("default storage path = %q, want /var/lib/rook/rook.db", cfg.Storage.Path)
	}
	if cfg.Storage.RetentionDays != 7 {
		t.Errorf("default retention = %d, want 7", cfg.Storage.RetentionDays)
	}
	if cfg.Socket.Path != "/run/rook.sock" {
		t.Errorf("default socket = %q, want /run/rook.sock", cfg.Socket.Path)
	}
	if cfg.Host.Proc != "/proc" {
		t.Errorf("default proc = %q, want /proc", cfg.Host.Proc)
	}
	if cfg.Host.Sys != "/sys" {
		t.Errorf("default sys = %q, want /sys", cfg.Host.Sys)
	}
	if cfg.Docker.Socket != "/var/run/docker.sock" {
		t.Errorf("default docker socket = %q, want /var/run/docker.sock", cfg.Docker.Socket)
	}
	if cfg.Collect.Interval.Duration != 10*time.Second {
		t.Errorf("default interval = %s, want 10s", cfg.Collect.Interval.Duration)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	_, err := LoadConfig("/nonexistent/config.toml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadConfigInvalidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("not valid [[[ toml"), 0644)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid TOML")
	}
}

func TestLoadConfigInvalidRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[storage]
retention_days = -1
`), 0644)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for negative retention")
	}
}

func TestDurationUnmarshal(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
		err   bool
	}{
		{"10s", 10 * time.Second, false},
		{"1m", 1 * time.Minute, false},
		{"2h30m", 2*time.Hour + 30*time.Minute, false},
		{"500ms", 500 * time.Millisecond, false},
		{"invalid", 0, true},
		{"", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			var d Duration
			err := d.UnmarshalText([]byte(tt.input))
			if tt.err {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d.Duration != tt.want {
				t.Errorf("got %s, want %s", d.Duration, tt.want)
			}
		})
	}
}
