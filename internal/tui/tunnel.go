package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Tunnel manages an SSH tunnel to a remote rook agent socket.
type Tunnel struct {
	cmd       *exec.Cmd
	localSock string
	done      chan error
	execFn    func(name string, args ...string) *exec.Cmd // injectable for testing
}

// NewTunnel creates an SSH tunnel forwarding a local Unix socket to the remote one.
// It blocks until the local socket appears or the timeout expires.
func NewTunnel(host, remoteSock string) (*Tunnel, error) {
	t := &Tunnel{
		execFn: exec.Command,
		done:   make(chan error, 1),
	}
	return t, t.start(host, remoteSock)
}

func (t *Tunnel) start(host, remoteSock string) error {
	// Create temp socket path.
	dir, err := os.MkdirTemp("", "rook-tunnel-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	t.localSock = filepath.Join(dir, "rook.sock")

	var stderr bytes.Buffer
	t.cmd = t.execFn("ssh", "-N", "-L", t.localSock+":"+remoteSock, host)
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

// LocalSocket returns the path to the local forwarded socket.
func (t *Tunnel) LocalSocket() string {
	return t.localSock
}

// Close terminates the SSH process and removes the temp socket.
func (t *Tunnel) Close() error {
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
