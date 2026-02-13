package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTunnelLocalSocketPath(t *testing.T) {
	// Test that a tunnel's local socket path is in a temp directory.
	tun := &Tunnel{
		localSock: "/tmp/tori-tunnel-test/tori.sock",
		done:      make(chan error, 1),
	}
	if tun.LocalSocket() != "/tmp/tori-tunnel-test/tori.sock" {
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

	dir, err := os.MkdirTemp("", "tori-tunnel-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	tun.localSock = filepath.Join(dir, "tori.sock")

	// Create a fake cmd that exits immediately.
	tun.cmd = tun.execFn("false")
	err = tun.start("badhost", "/run/tori.sock", SSHOptions{})

	if err == nil {
		t.Fatal("expected error for bad host")
	}
}

func TestTunnelRejectsHostStartingWithDash(t *testing.T) {
	_, err := NewTunnel("-oProxyCommand=evil", "/run/tori.sock")
	if err == nil {
		t.Fatal("should reject host starting with -")
	}
}

func TestTunnelRejectsIdentityFileStartingWithDash(t *testing.T) {
	_, err := NewTunnel("user@host", "/run/tori.sock", SSHOptions{
		IdentityFile: "-oProxyCommand=evil",
	})
	if err == nil {
		t.Fatal("should reject identity file starting with -")
	}
}

func TestTunnelSSHOptions(t *testing.T) {
	var gotArgs []string
	tun := &Tunnel{
		done: make(chan error, 1),
		execFn: func(name string, args ...string) *exec.Cmd {
			gotArgs = append([]string{name}, args...)
			return exec.Command("false")
		},
	}
	tun.cmd = tun.execFn("false")
	if err := tun.start("user@host", "/run/tori.sock", SSHOptions{
		Port:         2222,
		IdentityFile: "/home/user/.ssh/mykey",
	}); err == nil {
		t.Fatal("expected error from start()")
	}

	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "-p 2222") {
		t.Errorf("expected -p 2222 in args, got: %s", joined)
	}
	if !strings.Contains(joined, "-i /home/user/.ssh/mykey") {
		t.Errorf("expected -i flag in args, got: %s", joined)
	}
}

func TestTunnelSSHOptionsDefaults(t *testing.T) {
	var gotArgs []string
	tun := &Tunnel{
		done: make(chan error, 1),
		execFn: func(name string, args ...string) *exec.Cmd {
			gotArgs = append([]string{name}, args...)
			return exec.Command("false")
		},
	}
	tun.cmd = tun.execFn("false")
	if err := tun.start("user@host", "/run/tori.sock", SSHOptions{}); err == nil {
		t.Fatal("expected error from start()")
	}

	joined := strings.Join(gotArgs, " ")
	if strings.Contains(joined, "-p") {
		t.Errorf("should not include -p when port=0, got: %s", joined)
	}
	if strings.Contains(joined, "-i") {
		t.Errorf("should not include -i when identity_file empty, got: %s", joined)
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
