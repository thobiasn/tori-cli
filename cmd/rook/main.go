package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
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
	socketPath := fs.String("socket", "/run/rook.sock", "path to agent socket")
	fs.Parse(args)

	conn, err := net.Dial("unix", *socketPath)
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
