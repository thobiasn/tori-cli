package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
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

	var sockPath string
	var tunnel *tui.Tunnel

	positional := fs.Arg(0)

	switch {
	case *socketPath != "":
		sockPath = *socketPath

	case positional != "" && strings.Contains(positional, "@"):
		// Ad-hoc SSH: rook connect user@host
		var err error
		tunnel, err = tui.NewTunnel(positional, "/run/rook.sock")
		if err != nil {
			fmt.Fprintf(os.Stderr, "tunnel: %v\n", err)
			os.Exit(1)
		}
		sockPath = tunnel.LocalSocket()

	case positional != "":
		// Look up server name in config.
		cfgPath := *configPath
		if cfgPath == "" {
			cfgPath = tui.DefaultConfigPath()
		}
		cfg, err := tui.LoadConfig(cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load config %s: %v\n", cfgPath, err)
			os.Exit(1)
		}
		srv, ok := cfg.Servers[positional]
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown server %q in config\n", positional)
			os.Exit(1)
		}
		remoteSock := srv.Socket
		if remoteSock == "" {
			remoteSock = "/run/rook.sock"
		}
		if srv.Host != "" {
			tunnel, err = tui.NewTunnel(srv.Host, remoteSock)
			if err != nil {
				fmt.Fprintf(os.Stderr, "tunnel: %v\n", err)
				os.Exit(1)
			}
			sockPath = tunnel.LocalSocket()
		} else {
			sockPath = remoteSock
		}

	default:
		sockPath = "/run/rook.sock"
	}

	if tunnel != nil {
		defer tunnel.Close()
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}

	client := tui.NewClient(conn)
	defer client.Close()

	app := tui.NewApp(client)
	p := tea.NewProgram(app, tea.WithAltScreen())
	client.SetProgram(p)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui: %v\n", err)
		os.Exit(1)
	}
}
