package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thobiasn/rook/internal/agent"
	"github.com/thobiasn/rook/internal/tui"
)

func main() {
	// Askpass mode: when invoked as SSH_ASKPASS, relay prompt over IPC.
	if sock := os.Getenv("ROOK_ASKPASS_SOCK"); sock != "" {
		runAskpass(sock)
		return
	}

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

// runAskpass is the SSH_ASKPASS helper mode. SSH invokes the rook binary with
// the prompt as an argument. We connect to the IPC socket, send the prompt,
// and print the response to stdout.
func runAskpass(sock string) {
	prompt := strings.Join(os.Args[1:], " ")

	conn, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "askpass: dial: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Send prompt (newline-delimited).
	fmt.Fprintln(conn, prompt)

	// Read response.
	scanner := bufio.NewScanner(conn)
	if scanner.Scan() {
		fmt.Print(scanner.Text())
	} else {
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

	a, err := agent.New(cfg, *configPath)
	if err != nil {
		slog.Error("failed to create agent", "error", err)
		os.Exit(1)
	}

	// SIGHUP triggers config reload.
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	go func() {
		for range sighup {
			if err := a.Reload(); err != nil {
				slog.Error("config reload failed", "error", err)
			}
		}
	}()

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
		// Direct socket: single session, connect eagerly.
		runSingleSession("local", *socketPath, nil)

	case positional != "" && strings.Contains(positional, "@"):
		// Ad-hoc SSH: rook connect user@host — uses stdin for prompts (pre-TUI).
		tunnel, err := tui.NewTunnel(positional, "/run/rook/rook.sock")
		if err != nil {
			fmt.Fprintf(os.Stderr, "tunnel: %v\n", err)
			os.Exit(1)
		}
		runSingleSession(positional, tunnel.LocalSocket(), tunnel)

	case positional != "":
		// Named server from config — connect lazily with auto_connect=true.
		cfg := loadClientConfig(*configPath)
		srv, ok := cfg.Servers[positional]
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown server %q in config\n", positional)
			os.Exit(1)
		}
		srv.AutoConnect = true
		sess := tui.NewSession(positional, nil, nil)
		sess.Config = srv
		runSessions(map[string]*tui.Session{positional: sess})

	default:
		// No args: ensure config exists, create lazy sessions.
		cfgPath, err := tui.EnsureDefaultConfig(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "config: %v\n", err)
			os.Exit(1)
		}
		cfg, err := tui.LoadConfig(cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load config %s: %v\n", cfgPath, err)
			os.Exit(1)
		}
		if len(cfg.Servers) == 0 {
			fmt.Fprintf(os.Stderr, "no servers configured\n")
			os.Exit(1)
		}

		sessions := make(map[string]*tui.Session, len(cfg.Servers))
		for name, srv := range cfg.Servers {
			if srv.Host == "" {
				// Local socket — connect eagerly inline.
				sockPath := srv.Socket
				if sockPath == "" {
					sockPath = "/run/rook/rook.sock"
				}
				conn, err := net.Dial("unix", sockPath)
				if err != nil {
					sess := tui.NewSession(name, nil, nil)
					sess.Config = srv
					sess.Err = err
					sess.ConnState = tui.ConnError
					sess.ConnMsg = err.Error()
					sessions[name] = sess
					continue
				}
				client := tui.NewClient(conn, name)
				sess := tui.NewSession(name, client, nil)
				sess.Config = srv
				sess.ConnState = tui.ConnReady
				sess.ConnMsg = "connected"
				sessions[name] = sess
			} else {
				// SSH server — defer connection.
				sess := tui.NewSession(name, nil, nil)
				sess.Config = srv
				sessions[name] = sess
			}
		}
		runSessions(sessions)
	}
}

func loadClientConfig(path string) *tui.Config {
	cfgPath, err := tui.EnsureDefaultConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	cfg, err := tui.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config %s: %v\n", cfgPath, err)
		os.Exit(1)
	}
	return cfg
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
	sess.ConnState = tui.ConnReady
	sess.ConnMsg = "connected"
	runSessions(map[string]*tui.Session{name: sess})
}

func runSessions(sessions map[string]*tui.Session) {
	// Determine session order for active session selection.
	names := make([]string, 0, len(sessions))
	for name := range sessions {
		names = append(names, name)
	}
	sort.Strings(names)

	// Pick the first ready session as active, falling back to first in order.
	active := ""
	for _, name := range names {
		if sessions[name].ConnState == tui.ConnReady {
			active = name
			break
		}
	}

	app := tui.NewApp(sessions)

	// Override active session if we found a ready one that isn't the default.
	_ = active

	p := tea.NewProgram(app, tea.WithAltScreen())

	// Share the program reference with the app (via pointer) so connect
	// goroutines can send messages.
	app.SetProgram(p)

	// Start reading for already-connected sessions.
	for _, s := range sessions {
		if s.Client != nil {
			s.Client.SetProgram(p)
		}
	}

	model, err := p.Run()

	// Cleanup.
	for _, s := range sessions {
		if s.Client != nil {
			s.Client.Close()
		}
		if s.Tunnel != nil {
			s.Tunnel.Close()
		}
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "tui: %v\n", err)
		os.Exit(1)
	}
	if final, ok := model.(tui.App); ok && final.Err() != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", final.Err())
		os.Exit(1)
	}
}
