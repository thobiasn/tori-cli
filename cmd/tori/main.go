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
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thobiasn/tori-cli/internal/agent"
	"github.com/thobiasn/tori-cli/internal/tui"
)

// version is set via -ldflags at build time. GoReleaser fills this automatically.
var version = "dev"

func main() {
	// Askpass mode: when invoked as SSH_ASKPASS, relay prompt over IPC.
	if sock := os.Getenv("TORI_ASKPASS_SOCK"); sock != "" {
		runAskpass(sock)
		return
	}

	if len(os.Args) >= 2 && os.Args[1] == "--version" {
		fmt.Println("tori " + version)
		return
	}

	if len(os.Args) >= 2 && os.Args[1] == "agent" {
		runAgent(os.Args[2:])
	} else {
		runClient(os.Args[1:])
	}
}

// runAskpass is the SSH_ASKPASS helper mode. SSH invokes the tori binary with
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
	configPath := fs.String("config", "/etc/tori/config.toml", "path to config file")
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
		for {
			select {
			case <-ctx.Done():
				return
			case <-sighup:
				if err := a.Reload(); err != nil {
					slog.Error("config reload failed", "error", err)
				}
			}
		}
	}()

	if err := a.Run(ctx); err != nil {
		slog.Error("agent stopped with error", "error", err)
		os.Exit(1)
	}
}

// clientAction describes what runClient should do after parsing flags.
type clientAction struct {
	mode       string // "socket", "ssh", "config"
	socketPath string
	configPath string
	host       string
	remoteSock string
	sshOpts    tui.SSHOptions
}

// parseClientArgs parses the client CLI flags and returns the action to take.
func parseClientArgs(args []string) (*clientAction, error) {
	fs := flag.NewFlagSet("tori", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage:\n  tori [user@host] [flags]\n  tori agent [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	socketPath := fs.String("socket", "", "path to agent socket (direct connection)")
	configPath := fs.String("config", "", "path to client config")
	port := fs.Int("port", 0, "SSH port (default: 22)")
	identity := fs.String("identity", "", "SSH identity file")
	remoteSock := fs.String("remote-socket", "/run/tori/tori.sock", "remote agent socket path")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	positional := fs.Arg(0)

	// Go's flag package stops parsing at the first non-flag argument.
	// Re-parse trailing args so "user@host --port 2222" works.
	if rest := fs.Args(); len(rest) > 1 {
		if err := fs.Parse(rest[1:]); err != nil {
			return nil, err
		}
	}

	switch {
	case *socketPath != "":
		return &clientAction{
			mode:       "socket",
			socketPath: *socketPath,
		}, nil

	case positional != "" && strings.Contains(positional, "@"):
		return &clientAction{
			mode:       "ssh",
			host:       positional,
			remoteSock: *remoteSock,
			sshOpts: tui.SSHOptions{
				Port:         *port,
				IdentityFile: *identity,
			},
		}, nil

	default:
		return &clientAction{
			mode:       "config",
			configPath: *configPath,
		}, nil
	}
}

func runClient(args []string) {
	act, err := parseClientArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	switch act.mode {
	case "socket":
		conn, err := net.Dial("unix", act.socketPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "connect: %v\n", err)
			os.Exit(1)
		}
		client := tui.NewClient(conn, "local")
		sess := tui.NewSession("local", client, nil)
		sess.ConnState = tui.ConnReady
		sess.ConnMsg = "connected"
		runSessions(map[string]*tui.Session{"local": sess}, defaultDisplayConfig(), defaultTheme())

	case "ssh":
		sess := tui.NewSession(act.host, nil, nil)
		sess.Config = tui.ServerConfig{
			Host:         act.host,
			Socket:       act.remoteSock,
			Port:         act.sshOpts.Port,
			IdentityFile: act.sshOpts.IdentityFile,
			AutoConnect:  true,
		}
		runSessions(map[string]*tui.Session{act.host: sess}, defaultDisplayConfig(), defaultTheme())

	case "config":
		cfgPath, err := tui.EnsureDefaultConfig(act.configPath)
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
			fmt.Fprintf(os.Stderr, "No servers configured in %s\n\n", cfgPath)
			fmt.Fprintf(os.Stderr, "Add a server to the config file, or connect directly:\n")
			fmt.Fprintf(os.Stderr, "  tori user@host            # connect over SSH\n")
			fmt.Fprintf(os.Stderr, "  tori --socket /path.sock  # connect to local socket\n")
			os.Exit(1)
		}

		sessions := make(map[string]*tui.Session, len(cfg.Servers))
		for name, srv := range cfg.Servers {
			if srv.Host == "" {
				// Local socket — connect eagerly inline.
				sockPath := srv.Socket
				if sockPath == "" {
					sockPath = "/run/tori/tori.sock"
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
		runSessions(sessions, cfg.Display, tui.BuildTheme(cfg.Theme))
	}
}

// defaultDisplayConfig returns the default display config for direct connections
// that bypass the config file.
func defaultDisplayConfig() tui.DisplayConfig {
	return tui.DisplayConfig{DateFormat: "2006-01-02", TimeFormat: "15:04:05"}
}

// defaultTheme returns ANSI defaults for direct connections that bypass the config file.
func defaultTheme() tui.Theme {
	return tui.BuildTheme(tui.ThemeConfig{})
}

func runSessions(sessions map[string]*tui.Session, display tui.DisplayConfig, theme tui.Theme) {
	app := tui.NewApp(sessions, display, theme)

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
