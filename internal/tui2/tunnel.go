package tui2

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SSHOptions holds optional SSH connection parameters.
type SSHOptions struct {
	Port         int    // SSH port (0 = default)
	IdentityFile string // path to private key (empty = default)
}

// AskpassPromptFn is called when SSH needs interactive input (passphrase,
// host key verification). It blocks until the user responds. Return ("", error)
// to cancel.
type AskpassPromptFn func(prompt string) (string, error)

// Tunnel manages an SSH tunnel to a remote tori agent socket.
type Tunnel struct {
	cmd       *exec.Cmd
	localSock string
	done      chan error
	stderr    *bytes.Buffer // SSH stderr output for error reporting
	execFn    func(name string, args ...string) *exec.Cmd // injectable for testing

	// Askpass IPC state (only set when created via NewTunnelAskpass).
	askpassDir      string       // temp dir for IPC socket
	askpassListener net.Listener // IPC listener
	askpassDone     chan struct{}
}

// NewTunnel creates an SSH tunnel forwarding a local Unix socket to the remote one.
// It blocks until the local socket appears or the timeout expires.
// Uses stdin for SSH prompts — only suitable before the TUI starts.
func NewTunnel(host, remoteSock string, opts ...SSHOptions) (*Tunnel, error) {
	if strings.HasPrefix(host, "-") {
		return nil, fmt.Errorf("invalid host: %q", host)
	}
	var o SSHOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	t := &Tunnel{
		execFn: exec.Command,
		done:   make(chan error, 1),
	}
	return t, t.start(host, remoteSock, o)
}

func (t *Tunnel) start(host, remoteSock string, opts SSHOptions) error {
	// Create temp socket path.
	dir, err := os.MkdirTemp("", "tori-tunnel-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	t.localSock = filepath.Join(dir, "tori.sock")

	args := []string{"-N", "-C", "-o", "ControlPath=none"}
	if opts.Port > 0 {
		args = append(args, "-p", strconv.Itoa(opts.Port))
	}
	if opts.IdentityFile != "" {
		if strings.HasPrefix(opts.IdentityFile, "-") {
			os.RemoveAll(dir)
			return fmt.Errorf("invalid identity file: %q", opts.IdentityFile)
		}
		args = append(args, "-i", opts.IdentityFile)
	}
	args = append(args, "-L", t.localSock+":"+remoteSock, host)

	var stderr bytes.Buffer
	t.cmd = t.execFn("ssh", args...)
	t.cmd.Stdin = os.Stdin // allow passphrase prompts
	t.cmd.Stderr = &stderr

	if err := t.cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("start ssh: %w", err)
	}

	go func() {
		t.done <- t.cmd.Wait()
	}()

	// Poll for socket to appear.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-t.done:
			os.RemoveAll(dir)
			msg := stderr.String()
			if msg != "" {
				return fmt.Errorf("ssh exited: %s", msg)
			}
			if err != nil {
				return fmt.Errorf("ssh exited: %w", err)
			}
			return fmt.Errorf("ssh exited unexpectedly")
		default:
		}

		if _, err := os.Stat(t.localSock); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Timeout — kill and report.
	t.cmd.Process.Kill()
	<-t.done
	os.RemoveAll(dir)
	msg := stderr.String()
	if msg != "" {
		return fmt.Errorf("timeout waiting for ssh tunnel: %s", msg)
	}
	return fmt.Errorf("timeout waiting for ssh tunnel socket")
}

// NewTunnelAskpass creates an SSH tunnel that routes interactive prompts
// through promptFn instead of stdin. Returns immediately — call WaitReady
// to block until the tunnel is established.
func NewTunnelAskpass(host, remoteSock string, promptFn AskpassPromptFn, opts SSHOptions) (*Tunnel, error) {
	if strings.HasPrefix(host, "-") {
		return nil, fmt.Errorf("invalid host: %q", host)
	}

	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}

	t := &Tunnel{
		execFn:      exec.Command,
		done:        make(chan error, 1),
		askpassDone: make(chan struct{}),
	}

	// Create temp dir for both the tunnel socket and the askpass IPC socket.
	dir, err := os.MkdirTemp("", "tori-tunnel-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	t.localSock = filepath.Join(dir, "tori.sock")

	// Start askpass IPC listener.
	ipcSock := filepath.Join(dir, "askpass.sock")
	t.askpassDir = dir
	ln, err := net.Listen("unix", ipcSock)
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("listen askpass socket: %w", err)
	}
	t.askpassListener = ln

	go t.askpassLoop(promptFn)

	// Build SSH command.
	args := []string{"-N", "-C", "-o", "ControlPath=none"}
	if opts.Port > 0 {
		args = append(args, "-p", strconv.Itoa(opts.Port))
	}
	if opts.IdentityFile != "" {
		if strings.HasPrefix(opts.IdentityFile, "-") {
			ln.Close()
			os.RemoveAll(dir)
			return nil, fmt.Errorf("invalid identity file: %q", opts.IdentityFile)
		}
		args = append(args, "-i", opts.IdentityFile)
	}
	args = append(args, "-L", t.localSock+":"+remoteSock, host)

	t.cmd = t.execFn("ssh", args...)
	t.cmd.Env = append(os.Environ(),
		"SSH_ASKPASS="+self,
		"SSH_ASKPASS_REQUIRE=force",
		"TORI_ASKPASS_SOCK="+ipcSock,
		"DISPLAY=:0",
	)
	t.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	t.stderr = &bytes.Buffer{}
	t.cmd.Stderr = t.stderr

	if err := t.cmd.Start(); err != nil {
		ln.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("start ssh: %w", err)
	}

	go func() {
		t.done <- t.cmd.Wait()
	}()

	return t, nil
}

// WaitReady blocks until the tunnel's local socket appears, the SSH process
// exits, or ctx is cancelled.
func (t *Tunnel) WaitReady(ctx context.Context) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-t.done:
			// SSH exited — put the error back so Close() doesn't block.
			t.done <- err
			if t.stderr != nil {
				if msg := strings.TrimSpace(t.stderr.String()); msg != "" {
					return fmt.Errorf("ssh: %s", msg)
				}
			}
			if err != nil {
				return fmt.Errorf("ssh exited: %w", err)
			}
			return fmt.Errorf("ssh exited unexpectedly")
		case <-ticker.C:
			if _, err := os.Stat(t.localSock); err == nil {
				return nil
			}
		}
	}
}

// askpassLoop accepts connections on the IPC socket and handles prompts.
func (t *Tunnel) askpassLoop(promptFn AskpassPromptFn) {
	defer close(t.askpassDone)
	for {
		conn, err := t.askpassListener.Accept()
		if err != nil {
			return // listener closed
		}
		t.handleAskpassConn(conn, promptFn)
	}
}

// handleAskpassConn reads a prompt from the IPC connection, calls promptFn,
// and writes the response back.
func (t *Tunnel) handleAskpassConn(conn net.Conn, promptFn AskpassPromptFn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Minute))

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024), 1024)
	if !scanner.Scan() {
		return
	}
	prompt := scanner.Text()

	resp, err := promptFn(prompt)
	if err != nil {
		return // cancelled — SSH will see EOF and fail
	}
	conn.Write([]byte(resp + "\n"))
}

// LocalSocket returns the path to the local forwarded socket.
func (t *Tunnel) LocalSocket() string {
	return t.localSock
}

// Close terminates the SSH process, stops the askpass listener, and removes
// temp files.
func (t *Tunnel) Close() error {
	if t.askpassListener != nil {
		t.askpassListener.Close()
		<-t.askpassDone
	}
	if t.cmd != nil && t.cmd.Process != nil {
		t.cmd.Process.Signal(os.Interrupt)
		select {
		case <-t.done:
		case <-time.After(3 * time.Second):
			t.cmd.Process.Kill()
			<-t.done
		}
	}
	if t.localSock != "" {
		os.RemoveAll(filepath.Dir(t.localSock))
	}
	return nil
}
