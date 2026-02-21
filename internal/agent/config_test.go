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

	if cfg.Storage.Path != "/var/lib/tori/tori.db" {
		t.Errorf("default storage path = %q, want /var/lib/tori/tori.db", cfg.Storage.Path)
	}
	if cfg.Storage.RetentionDays != 7 {
		t.Errorf("default retention = %d, want 7", cfg.Storage.RetentionDays)
	}
	if cfg.Socket.Path != "/run/tori/tori.sock" {
		t.Errorf("default socket = %q, want /run/tori/tori.sock", cfg.Socket.Path)
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

func TestLoadConfigWithAlerts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.high_cpu]
condition = "host.cpu_percent > 90"
for = "30s"
severity = "critical"
actions = ["notify"]

[alerts.disk_full]
condition = "host.disk_percent > 90"
severity = "warning"
actions = ["notify"]

[[notify.webhooks]]
enabled = true
url = "https://example.com/hook"
`), 0644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Alerts) != 2 {
		t.Errorf("alerts count = %d, want 2", len(cfg.Alerts))
	}
	ac := cfg.Alerts["high_cpu"]
	if ac.Condition != "host.cpu_percent > 90" {
		t.Errorf("condition = %q", ac.Condition)
	}
	if ac.For.Duration != 30*time.Second {
		t.Errorf("for = %s, want 30s", ac.For.Duration)
	}
	if ac.Severity != "critical" {
		t.Errorf("severity = %q, want critical", ac.Severity)
	}
	if len(cfg.Notify.Webhooks) != 1 || !cfg.Notify.Webhooks[0].Enabled {
		t.Error("webhook should be enabled")
	}
}

func TestLoadConfigAlertInvalidSeverity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.bad]
condition = "host.cpu_percent > 90"
severity = "info"
actions = ["notify"]
`), 0644)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid severity")
	}
}

func TestLoadConfigAlertInvalidCondition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.bad]
condition = "not a valid condition"
severity = "warning"
actions = ["notify"]
`), 0644)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid condition")
	}
}

func TestLoadConfigAlertInvalidAction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.bad]
condition = "container.state == 'exited'"
severity = "critical"
actions = ["restart"]
`), 0644)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid action")
	}
}

func TestLoadConfigAlertNoActions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.bad]
condition = "host.cpu_percent > 90"
severity = "warning"
actions = []
`), 0644)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for empty actions")
	}
}

func TestLoadConfigAlertUnknownField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.bad]
condition = "host.unknown_field > 2"
severity = "warning"
actions = ["notify"]
`), 0644)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestLoadConfigAlertStringOpOnState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.bad]
condition = "container.state > 'exited'"
severity = "warning"
actions = ["notify"]
`), 0644)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for > operator on string field")
	}
}

func TestWebhookValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr bool
	}{
		{
			name: "valid webhook",
			config: `
[[notify.webhooks]]
enabled = true
url = "https://example.com/hook"
`,
		},
		{
			name: "disabled webhook no url",
			config: `
[[notify.webhooks]]
enabled = false
`,
		},
		{
			name: "enabled webhook missing url",
			config: `
[[notify.webhooks]]
enabled = true
`,
			wantErr: true,
		},
		{
			name: "webhook with custom headers",
			config: `
[[notify.webhooks]]
enabled = true
url = "https://example.com/hook"
headers = { "Authorization" = "Bearer xxx" }
`,
		},
		{
			name: "webhook with valid template",
			config: `
[[notify.webhooks]]
enabled = true
url = "https://example.com/hook"
template = '{"msg":"{{.Subject}}"}'
`,
		},
		{
			name: "webhook with invalid template",
			config: `
[[notify.webhooks]]
enabled = true
url = "https://example.com/hook"
template = '{{.Invalid'
`,
			wantErr: true,
		},
		{
			name: "multiple webhooks",
			config: `
[[notify.webhooks]]
enabled = true
url = "https://example.com/hook1"

[[notify.webhooks]]
enabled = true
url = "https://example.com/hook2"
`,
		},
		{
			name: "webhook invalid scheme",
			config: `
[[notify.webhooks]]
enabled = true
url = "ftp://example.com/hook"
`,
			wantErr: true,
		},
		{
			name: "webhook http scheme allowed",
			config: `
[[notify.webhooks]]
enabled = true
url = "http://example.com/hook"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			os.WriteFile(path, []byte(tt.config), 0644)
			_, err := LoadConfig(path)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestEmailValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr bool
	}{
		{
			name: "valid email config",
			config: `
[notify.email]
enabled = true
smtp_host = "smtp.example.com"
smtp_port = 587
from = "alerts@example.com"
to = ["admin@example.com"]
`,
		},
		{
			name: "disabled email no fields required",
			config: `
[notify.email]
enabled = false
`,
		},
		{
			name: "enabled missing smtp_host",
			config: `
[notify.email]
enabled = true
smtp_port = 587
from = "alerts@example.com"
to = ["admin@example.com"]
`,
			wantErr: true,
		},
		{
			name: "enabled missing from",
			config: `
[notify.email]
enabled = true
smtp_host = "smtp.example.com"
smtp_port = 587
to = ["admin@example.com"]
`,
			wantErr: true,
		},
		{
			name: "enabled empty to",
			config: `
[notify.email]
enabled = true
smtp_host = "smtp.example.com"
smtp_port = 587
from = "alerts@example.com"
to = []
`,
			wantErr: true,
		},
		{
			name: "enabled port 0",
			config: `
[notify.email]
enabled = true
smtp_host = "smtp.example.com"
smtp_port = 0
from = "alerts@example.com"
to = ["admin@example.com"]
`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			os.WriteFile(path, []byte(tt.config), 0644)
			_, err := LoadConfig(path)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadConfigAlertDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.high_cpu]
condition = "host.cpu_percent > 90"
severity = "critical"
actions = ["notify"]
`), 0644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	ac := cfg.Alerts["high_cpu"]
	if ac.Cooldown.Duration != 5*time.Minute {
		t.Errorf("cooldown = %s, want 5m", ac.Cooldown.Duration)
	}
	if ac.NotifyCooldown.Duration != 5*time.Minute {
		t.Errorf("notify_cooldown = %s, want 5m", ac.NotifyCooldown.Duration)
	}
}

func TestLoadConfigAlertCooldownExplicit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.high_cpu]
condition = "host.cpu_percent > 90"
cooldown = "10m"
notify_cooldown = "0s"
severity = "critical"
actions = ["notify"]
`), 0644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	ac := cfg.Alerts["high_cpu"]
	if ac.Cooldown.Duration != 10*time.Minute {
		t.Errorf("cooldown = %s, want 10m", ac.Cooldown.Duration)
	}
	if ac.NotifyCooldown.Duration != 0 {
		t.Errorf("notify_cooldown = %s, want 0s (explicitly disabled)", ac.NotifyCooldown.Duration)
	}
}

func TestLoadConfigAlertNegativeDurations(t *testing.T) {
	tests := []struct {
		name   string
		config string
	}{
		{
			name: "negative for",
			config: `
[alerts.bad]
condition = "host.cpu_percent > 90"
for = "-5m"
severity = "critical"
actions = ["notify"]
`,
		},
		{
			name: "negative cooldown",
			config: `
[alerts.bad]
condition = "host.cpu_percent > 90"
cooldown = "-5m"
severity = "critical"
actions = ["notify"]
`,
		},
		{
			name: "negative notify_cooldown",
			config: `
[alerts.bad]
condition = "host.cpu_percent > 90"
notify_cooldown = "-5m"
severity = "critical"
actions = ["notify"]
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			os.WriteFile(path, []byte(tt.config), 0644)
			_, err := LoadConfig(path)
			if err == nil {
				t.Fatal("expected error for negative duration")
			}
		})
	}
}

func TestLoadConfigLogAlert(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.error_spike]
condition = "log.count > 5"
match = "error"
window = "5m"
severity = "warning"
actions = ["notify"]
`), 0644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	ac := cfg.Alerts["error_spike"]
	if ac.Match != "error" {
		t.Errorf("match = %q, want error", ac.Match)
	}
	if ac.Window.Duration != 5*time.Minute {
		t.Errorf("window = %s, want 5m", ac.Window.Duration)
	}
}

func TestLoadConfigLogAlertRegex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.oom]
condition = "log.count > 0"
match = "OOM|out of memory"
match_regex = true
window = "10m"
severity = "critical"
actions = ["notify"]
`), 0644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	ac := cfg.Alerts["oom"]
	if !ac.MatchRegex {
		t.Error("match_regex should be true")
	}
}

func TestLoadConfigLogAlertMissingMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.bad]
condition = "log.count > 5"
window = "5m"
severity = "warning"
actions = ["notify"]
`), 0644)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for log rule without match")
	}
}

func TestLoadConfigLogAlertMissingWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.bad]
condition = "log.count > 5"
match = "error"
severity = "warning"
actions = ["notify"]
`), 0644)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for log rule without window")
	}
}

func TestLoadConfigLogAlertInvalidRegex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.bad]
condition = "log.count > 5"
match = "[invalid"
match_regex = true
window = "5m"
severity = "warning"
actions = ["notify"]
`), 0644)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestLoadConfigMatchOnNonLogRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.bad]
condition = "host.cpu_percent > 90"
match = "error"
severity = "warning"
actions = ["notify"]
`), 0644)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for match on non-log rule")
	}
}

func TestLoadConfigWindowOnNonLogRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[alerts.bad]
condition = "host.cpu_percent > 90"
window = "5m"
severity = "warning"
actions = ["notify"]
`), 0644)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for window on non-log rule")
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
