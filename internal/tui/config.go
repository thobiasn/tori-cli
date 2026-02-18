package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/lipgloss"
)

// ServerConfig describes how to reach a single tori agent.
type ServerConfig struct {
	Host         string `toml:"host"`          // user@host (SSH)
	Socket       string `toml:"socket"`        // /run/tori/tori.sock
	Port         int    `toml:"port"`          // SSH port (default: 22)
	IdentityFile string `toml:"identity_file"` // path to SSH private key
	AutoConnect  bool   `toml:"auto_connect"`  // connect on startup (default: false)
}

// DisplayConfig controls how dates and times are rendered in the TUI.
type DisplayConfig struct {
	DateFormat string `toml:"date_format"`
	TimeFormat string `toml:"time_format"`
}

// ThemeConfig holds optional color overrides. Empty strings use ANSI defaults.
// Values can be ANSI numbers ("1"), 256-palette numbers ("196"), or hex ("#ff0000").
type ThemeConfig struct {
	Fg         string `toml:"fg"`
	FgDim      string `toml:"fg_dim"`
	FgBright   string `toml:"fg_bright"`
	Border     string `toml:"border"`
	Accent     string `toml:"accent"`
	Healthy    string `toml:"healthy"`
	Warning    string `toml:"warning"`
	Critical   string `toml:"critical"`
	DebugLevel string `toml:"debug_level"`
	InfoLevel  string `toml:"info_level"`
	GraphCPU   string `toml:"graph_cpu"`
	GraphMem   string `toml:"graph_mem"`
}

// Config is the client-side configuration.
type Config struct {
	Servers map[string]ServerConfig `toml:"servers"`
	Display DisplayConfig           `toml:"display"`
	Theme   ThemeConfig             `toml:"theme"`
}

// DefaultConfigPath returns $XDG_CONFIG_HOME/tori/config.toml,
// falling back to ~/.config/tori/config.toml if unset.
func DefaultConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "tori", "config.toml")
}

const defaultConfigContent = `# Tori client configuration.
# Add servers below. Each server needs either a socket path (local)
# or a host (SSH). See README.md for full documentation.
#
# [servers.local]
# socket = "/run/tori/tori.sock"
#
# [servers.production]
# host = "user@example.com"
# port = 22
# socket = "/run/tori/tori.sock"
# identity_file = "~/.ssh/id_ed25519"
# auto_connect = true
#
# [display]
# date_format = "2006-01-02"   # Go time layout
# time_format = "15:04:05"     # Go time layout
#
# [theme]
# Colors default to ANSI (0-15) so the TUI inherits your terminal theme.
# Override with ANSI numbers, 256-palette numbers, or hex values.
#
# ANSI defaults:
# fg = "7"               # normal white
# fg_dim = "8"           # bright black
# fg_bright = "15"       # bright white
# border = "8"           # bright black
# accent = "4"           # blue
# healthy = "2"          # green
# warning = "3"          # yellow
# critical = "1"         # red
# debug_level = "8"      # bright black
# info_level = "7"       # normal white
# graph_cpu = "12"       # bright blue
# graph_mem = "13"       # bright magenta
#
# Example: Tokyo Night (hex overrides)
# fg = "#a9b1d6"
# fg_dim = "#3b4261"
# fg_bright = "#c0caf5"
# border = "#292e42"
# accent = "#7aa2f7"
# healthy = "#9ece6a"
# warning = "#e0af68"
# critical = "#f7768e"
# debug_level = "#414769"
# info_level = "#505a85"
# graph_cpu = "#7dcfff"
# graph_mem = "#bb9af7"
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
	if cfg.Display.DateFormat == "" {
		cfg.Display.DateFormat = "2006-01-02"
	}
	if cfg.Display.TimeFormat == "" {
		cfg.Display.TimeFormat = "15:04:05"
	}
	for name, srv := range cfg.Servers {
		if srv.Socket == "" && srv.Host == "" {
			return nil, fmt.Errorf("load config: server %q missing socket path", name)
		}
	}
	return &cfg, nil
}

// BuildTheme returns a Theme starting from ANSI defaults with any
// non-empty ThemeConfig fields applied as overrides.
func BuildTheme(tc ThemeConfig) Theme {
	t := TerminalTheme()
	override := func(dst *lipgloss.Color, src string) {
		if src != "" {
			*dst = lipgloss.Color(src)
		}
	}
	override(&t.Fg, tc.Fg)
	override(&t.FgDim, tc.FgDim)
	override(&t.FgBright, tc.FgBright)
	override(&t.Border, tc.Border)
	override(&t.Accent, tc.Accent)
	override(&t.Healthy, tc.Healthy)
	override(&t.Warning, tc.Warning)
	override(&t.Critical, tc.Critical)
	override(&t.DebugLevel, tc.DebugLevel)
	override(&t.InfoLevel, tc.InfoLevel)
	override(&t.GraphCPU, tc.GraphCPU)
	override(&t.GraphMem, tc.GraphMem)
	return t
}
