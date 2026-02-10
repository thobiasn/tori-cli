package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thobiasn/rook/internal/agent"
	"github.com/thobiasn/rook/internal/tui"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: rook <agent|connect> [flags]\n")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "agent":
		runAgent(os.Args[2:])
	case "connect":
		runConnect(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\nusage: rook <agent|connect> [flags]\n", os.Args[1])
		os.Exit(1)
	}
}

func runAgent(args []string) {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	configPath := fs.String("config", "/etc/rook/config.toml", "path to config file")
	fs.Parse(args)

	cfg, err := agent.LoadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := agent.New(cfg)
	if err != nil {
		slog.Error("failed to create agent", "error", err)
		os.Exit(1)
	}

	if err := a.Run(ctx); err != nil {
		slog.Error("agent stopped with error", "error", err)
		os.Exit(1)
	}
}

func runConnect(args []string) {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	socketPath := fs.String("socket", "", "path to agent socket (direct connection)")
	configPath := fs.String("config", "", "path to client config")
	fs.Parse(args)

	positional := fs.Arg(0)

	switch {
	case *socketPath != "":
		// Direct socket: single session.
		runSingleSession("local", *socketPath, nil)

	case positional != "" && strings.Contains(positional, "@"):
		// Ad-hoc SSH: rook connect user@host
		tunnel, err := tui.NewTunnel(positional, "/run/rook.sock")
		if err != nil {
			fmt.Fprintf(os.Stderr, "tunnel: %v\n", err)
			os.Exit(1)
		}
		runSingleSession(positional, tunnel.LocalSocket(), tunnel)

	case positional != "":
		// Named server from config.
		cfg := loadClientConfig(*configPath)
		srv, ok := cfg.Servers[positional]
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown server %q in config\n", positional)
			os.Exit(1)
		}
		sess, err := connectServer(positional, srv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "connect %s: %v\n", positional, err)
			os.Exit(1)
		}
		runSessions(map[string]*tui.Session{positional: sess})

	default:
		// No args: try config for multi-server, fall back to default socket.
		cfgPath := *configPath
		if cfgPath == "" {
			cfgPath = tui.DefaultConfigPath()
		}
		cfg, err := tui.LoadConfig(cfgPath)
		if err != nil {
			// No config — connect to default socket.
			runSingleSession("local", "/run/rook.sock", nil)
			return
		}
		sessions := connectAll(cfg)
		if len(sessions) == 0 {
			fmt.Fprintf(os.Stderr, "failed to connect to any server\n")
			os.Exit(1)
		}
		runSessions(sessions)
	}
}

func loadClientConfig(path string) *tui.Config {
	if path == "" {
		path = tui.DefaultConfigPath()
	}
	cfg, err := tui.LoadConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config %s: %v\n", path, err)
		os.Exit(1)
	}
	return cfg
}

func connectServer(name string, srv tui.ServerConfig) (*tui.Session, error) {
	remoteSock := srv.Socket
	if remoteSock == "" {
		remoteSock = "/run/rook.sock"
	}

	var tunnel *tui.Tunnel
	var sockPath string

	if srv.Host != "" {
		var err error
		tunnel, err = tui.NewTunnel(srv.Host, remoteSock, tui.SSHOptions{
			Port:         srv.Port,
			IdentityFile: srv.IdentityFile,
		})
		if err != nil {
			return nil, fmt.Errorf("tunnel: %w", err)
		}
		sockPath = tunnel.LocalSocket()
	} else {
		sockPath = remoteSock
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		if tunnel != nil {
			tunnel.Close()
		}
		return nil, fmt.Errorf("dial: %w", err)
	}

	client := tui.NewClient(conn, name)
	return tui.NewSession(name, client, tunnel), nil
}

func connectAll(cfg *tui.Config) map[string]*tui.Session {
	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)

	var mu sync.Mutex
	sessions := make(map[string]*tui.Session)
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(n string, srv tui.ServerConfig) {
			defer wg.Done()
			sess, err := connectServer(n, srv)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: %s: %v\n", n, err)
				return
			}
			mu.Lock()
			sessions[n] = sess
			mu.Unlock()
		}(name, cfg.Servers[name])
	}
	wg.Wait()
	return sessions
}

func runSingleSession(name, sockPath string, tunnel *tui.Tunnel) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		if tunnel != nil {
			tunnel.Close()
		}
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}

	client := tui.NewClient(conn, name)
	sess := tui.NewSession(name, client, tunnel)
	runSessions(map[string]*tui.Session{name: sess})
}

func runSessions(sessions map[string]*tui.Session) {
	app := tui.NewApp(sessions)
	p := tea.NewProgram(app, tea.WithAltScreen())

	for _, s := range sessions {
		s.Client.SetProgram(p)
	}

	_, err := p.Run()

	// Cleanup.
	for _, s := range sessions {
		s.Client.Close()
		if s.Tunnel != nil {
			s.Tunnel.Close()
		}
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "tui: %v\n", err)
		os.Exit(1)
	}
}
