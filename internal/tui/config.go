package tui

import (
	"errors"
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
	AutoConnect  bool   `toml:"auto_connect"`  // connect on startup (default: false)
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

const defaultConfigContent = `# Rook client configuration.
# Add servers below. Each server needs either a socket path (local)
# or a host (SSH). See README.md for full documentation.
#
# Examples:
#   [servers.local]
#   socket = "/run/rook/rook.sock"
#
#   [servers.production]
#   host = "user@example.com"
#   # port = 22
#   # socket = "/run/rook/rook.sock"
#   # identity_file = "~/.ssh/id_ed25519"
#   # auto_connect = true

[servers.local]
socket = "/run/rook/rook.sock"
`

// EnsureDefaultConfig creates the default config file if it does not exist.
// Returns the path to the config file.
func EnsureDefaultConfig(path string) (string, error) {
	if path == "" {
		path = DefaultConfigPath()
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(defaultConfigContent), 0o644); err != nil {
		return "", fmt.Errorf("write default config: %w", err)
	}
	return path, nil
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
