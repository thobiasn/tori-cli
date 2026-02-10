package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTunnelLocalSocketPath(t *testing.T) {
	// Test that a tunnel's local socket path is in a temp directory.
	tun := &Tunnel{
		localSock: "/tmp/rook-tunnel-test/rook.sock",
		done:      make(chan error, 1),
	}
	if tun.LocalSocket() != "/tmp/rook-tunnel-test/rook.sock" {
		t.Errorf("unexpected local socket: %s", tun.LocalSocket())
	}
}

func TestTunnelStartFailsOnBadHost(t *testing.T) {
	// Create a tunnel to a non-existent host — should fail with timeout or ssh error.
	// We use a very short timeout by injecting a failing command.
	tun := &Tunnel{
		done: make(chan error, 1),
		execFn: func(name string, args ...string) *exec.Cmd {
			// Return a command that exits immediately with an error.
			return exec.Command("false")
		},
	}

	dir, err := os.MkdirTemp("", "rook-tunnel-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	tun.localSock = filepath.Join(dir, "rook.sock")

	// Create a fake cmd that exits immediately.
	tun.cmd = tun.execFn("false")
	err = tun.start("badhost", "/run/rook.sock")

	if err == nil {
		t.Fatal("expected error for bad host")
	}
}

func TestTunnelRejectsHostStartingWithDash(t *testing.T) {
	_, err := NewTunnel("-oProxyCommand=evil", "/run/rook.sock")
	if err == nil {
		t.Fatal("should reject host starting with -")
	}
}

func TestTunnelCloseNilProcess(t *testing.T) {
	tun := &Tunnel{
		done: make(chan error, 1),
	}
	// Should not panic.
	err := tun.Close()
	if err != nil {
		t.Errorf("Close() error: %v", err)
	}
}
