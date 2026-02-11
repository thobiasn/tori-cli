package tui

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// ServerConfig describes how to reach a single rook agent.
type ServerConfig struct {
	Host         string `toml:"host"`          // user@host (SSH)
	Socket       string `toml:"socket"`        // /run/rook/rook.sock
	Port         int    `toml:"port"`          // SSH port (default: 22)
	IdentityFile string `toml:"identity_file"` // path to SSH private key
}

// Config is the client-side configuration.
type Config struct {
	Servers map[string]ServerConfig `toml:"servers"`
}

// DefaultConfigPath returns ~/.config/rook/config.toml using os.UserConfigDir.
func DefaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(os.Getenv("HOME"), ".config", "rook", "config.toml")
	}
	return filepath.Join(dir, "rook", "config.toml")
}

// LoadConfig reads and parses a TOML client config file.
func LoadConfig(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if len(cfg.Servers) == 0 {
		return nil, fmt.Errorf("load config: no servers defined")
	}
	for name, srv := range cfg.Servers {
		if srv.Socket == "" && srv.Host == "" {
			return nil, fmt.Errorf("load config: server %q missing socket path", name)
		}
	}
	return &cfg, nil
}
