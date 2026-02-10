package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/thobiasn/rook/internal/agent"
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
		fmt.Fprintf(os.Stderr, "connect: not implemented\n")
		os.Exit(1)
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
