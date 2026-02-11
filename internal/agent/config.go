package agent

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/BurntSushi/toml"
)

// Duration wraps time.Duration for TOML string parsing ("10s", "1m").
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalText(text []byte) error {
	var err error
	d.Duration, err = time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	return nil
}

type Config struct {
	Storage StorageConfig          `toml:"storage"`
	Socket  SocketConfig           `toml:"socket"`
	Host    HostConfig             `toml:"host"`
	Docker  DockerConfig           `toml:"docker"`
	Collect CollectConfig          `toml:"collect"`
	Alerts  map[string]AlertConfig `toml:"alerts"`
	Notify  NotifyConfig           `toml:"notify"`
}

type AlertConfig struct {
	Condition   string   `toml:"condition"`
	For         Duration `toml:"for"`
	Severity    string   `toml:"severity"`
	Actions     []string `toml:"actions"`
	MaxRestarts int      `toml:"max_restarts"`
}

type NotifyConfig struct {
	Email    EmailConfig     `toml:"email"`
	Webhooks []WebhookConfig `toml:"webhooks"`
}

type EmailConfig struct {
	Enabled  bool     `toml:"enabled"`
	SMTPHost string   `toml:"smtp_host"`
	SMTPPort int      `toml:"smtp_port"`
	From     string   `toml:"from"`
	To       []string `toml:"to"`
}

type WebhookConfig struct {
	Enabled  bool              `toml:"enabled"`
	URL      string            `toml:"url"`
	Headers  map[string]string `toml:"headers"`
	Template string            `toml:"template"`
}

type StorageConfig struct {
	Path          string `toml:"path"`
	RetentionDays int    `toml:"retention_days"`
}

type SocketConfig struct {
	Path string `toml:"path"`
}

type HostConfig struct {
	Proc string `toml:"proc"`
	Sys  string `toml:"sys"`
}

type DockerConfig struct {
	Socket  string   `toml:"socket"`
	Include []string `toml:"include"`
	Exclude []string `toml:"exclude"`
}

type CollectConfig struct {
	Interval Duration `toml:"interval"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	setDefaults(cfg)

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}

func setDefaults(cfg *Config) {
	if cfg.Storage.Path == "" {
		cfg.Storage.Path = "/var/lib/rook/rook.db"
	}
	if cfg.Storage.RetentionDays == 0 {
		cfg.Storage.RetentionDays = 7
	}
	if cfg.Socket.Path == "" {
		cfg.Socket.Path = "/run/rook/rook.sock"
	}
	if cfg.Host.Proc == "" {
		cfg.Host.Proc = "/proc"
	}
	if cfg.Host.Sys == "" {
		cfg.Host.Sys = "/sys"
	}
	if cfg.Docker.Socket == "" {
		cfg.Docker.Socket = "/var/run/docker.sock"
	}
	if cfg.Collect.Interval.Duration == 0 {
		cfg.Collect.Interval.Duration = 10 * time.Second
	}
}

func validate(cfg *Config) error {
	if cfg.Storage.RetentionDays < 1 {
		return fmt.Errorf("retention_days must be >= 1, got %d", cfg.Storage.RetentionDays)
	}
	if cfg.Collect.Interval.Duration < 1*time.Second {
		return fmt.Errorf("collect interval must be >= 1s, got %s", cfg.Collect.Interval.Duration)
	}
	for name, ac := range cfg.Alerts {
		if err := validateAlert(name, &ac); err != nil {
			return err
		}
	}
	for i, wh := range cfg.Notify.Webhooks {
		if err := validateWebhook(i, &wh); err != nil {
			return err
		}
	}
	return nil
}

func validateWebhook(idx int, wh *WebhookConfig) error {
	if !wh.Enabled {
		return nil
	}
	if wh.URL == "" {
		return fmt.Errorf("webhook[%d]: url is required when enabled", idx)
	}
	u, err := url.Parse(wh.URL)
	if err != nil {
		return fmt.Errorf("webhook[%d]: invalid url: %w", idx, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook[%d]: url scheme must be http or https", idx)
	}
	for key, val := range wh.Headers {
		if strings.ContainsAny(key, "\r\n") {
			return fmt.Errorf("webhook[%d]: header key contains invalid characters", idx)
		}
		if strings.ContainsAny(val, "\r\n") {
			return fmt.Errorf("webhook[%d]: header value contains invalid characters", idx)
		}
	}
	if wh.Template != "" {
		if _, err := template.New("").Parse(wh.Template); err != nil {
			return fmt.Errorf("webhook[%d]: invalid template: %w", idx, err)
		}
	}
	return nil
}

func validateAlert(name string, ac *AlertConfig) error {
	if _, err := parseCondition(ac.Condition); err != nil {
		return fmt.Errorf("alert %q: %w", name, err)
	}
	switch ac.Severity {
	case "warning", "critical":
	default:
		return fmt.Errorf("alert %q: severity must be \"warning\" or \"critical\", got %q", name, ac.Severity)
	}
	if len(ac.Actions) == 0 {
		return fmt.Errorf("alert %q: at least one action required", name)
	}
	for _, a := range ac.Actions {
		switch a {
		case "notify", "restart":
		default:
			return fmt.Errorf("alert %q: unknown action %q (must be \"notify\" or \"restart\")", name, a)
		}
		if a == "restart" && ac.MaxRestarts < 1 {
			return fmt.Errorf("alert %q: restart action requires max_restarts > 0", name)
		}
	}
	return nil
}
