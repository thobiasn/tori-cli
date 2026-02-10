package agent

import (
	"fmt"
	"os"
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
	Storage StorageConfig `toml:"storage"`
	Socket  SocketConfig  `toml:"socket"`
	Host    HostConfig    `toml:"host"`
	Docker  DockerConfig  `toml:"docker"`
	Collect CollectConfig `toml:"collect"`
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
		cfg.Socket.Path = "/run/rook.sock"
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
	return nil
}
