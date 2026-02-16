package main

import (
	"testing"

	tui "github.com/thobiasn/tori-cli/internal/tui2"
)

func TestParseClientArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    clientAction
		wantErr bool
	}{
		{
			name: "no args defaults to config mode",
			args: []string{},
			want: clientAction{mode: "config"},
		},
		{
			name: "user@host gives ssh mode",
			args: []string{"deploy@10.0.0.1"},
			want: clientAction{
				mode:       "ssh",
				host:       "deploy@10.0.0.1",
				remoteSock: "/run/tori/tori.sock",
			},
		},
		{
			name: "user@host with port",
			args: []string{"deploy@10.0.0.1", "--port", "2222"},
			want: clientAction{
				mode:       "ssh",
				host:       "deploy@10.0.0.1",
				remoteSock: "/run/tori/tori.sock",
				sshOpts:    tui.SSHOptions{Port: 2222},
			},
		},
		{
			name: "user@host with identity",
			args: []string{"deploy@10.0.0.1", "--identity", "/home/me/.ssh/id_ed25519"},
			want: clientAction{
				mode:       "ssh",
				host:       "deploy@10.0.0.1",
				remoteSock: "/run/tori/tori.sock",
				sshOpts:    tui.SSHOptions{IdentityFile: "/home/me/.ssh/id_ed25519"},
			},
		},
		{
			name: "user@host with custom remote socket",
			args: []string{"deploy@10.0.0.1", "--remote-socket", "/tmp/tori.sock"},
			want: clientAction{
				mode:       "ssh",
				host:       "deploy@10.0.0.1",
				remoteSock: "/tmp/tori.sock",
			},
		},
		{
			name: "user@host with all ssh flags",
			args: []string{"root@prod.example.com", "--port", "2222", "--identity", "/keys/id", "--remote-socket", "/custom/sock"},
			want: clientAction{
				mode:       "ssh",
				host:       "root@prod.example.com",
				remoteSock: "/custom/sock",
				sshOpts:    tui.SSHOptions{Port: 2222, IdentityFile: "/keys/id"},
			},
		},
		{
			name: "direct socket mode",
			args: []string{"--socket", "/run/tori/tori.sock"},
			want: clientAction{
				mode:       "socket",
				socketPath: "/run/tori/tori.sock",
			},
		},
		{
			name: "config mode with custom path",
			args: []string{"--config", "/home/me/.config/tori/custom.toml"},
			want: clientAction{
				mode:       "config",
				configPath: "/home/me/.config/tori/custom.toml",
			},
		},
		{
			name: "socket takes precedence over positional",
			args: []string{"--socket", "/tmp/sock", "user@host"},
			want: clientAction{
				mode:       "socket",
				socketPath: "/tmp/sock",
			},
		},
		{
			name: "flags before positional",
			args: []string{"--port", "2222", "deploy@10.0.0.1"},
			want: clientAction{
				mode:       "ssh",
				host:       "deploy@10.0.0.1",
				remoteSock: "/run/tori/tori.sock",
				sshOpts:    tui.SSHOptions{Port: 2222},
			},
		},
		{
			name: "positional without @ falls through to config",
			args: []string{"notahost"},
			want: clientAction{mode: "config"},
		},
		{
			name:    "unknown flag returns error",
			args:    []string{"--bogus"},
			wantErr: true,
		},
		{
			name:    "invalid port returns error",
			args:    []string{"--port", "abc"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseClientArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.mode != tt.want.mode {
				t.Errorf("mode = %q, want %q", got.mode, tt.want.mode)
			}
			if got.socketPath != tt.want.socketPath {
				t.Errorf("socketPath = %q, want %q", got.socketPath, tt.want.socketPath)
			}
			if got.configPath != tt.want.configPath {
				t.Errorf("configPath = %q, want %q", got.configPath, tt.want.configPath)
			}
			if got.host != tt.want.host {
				t.Errorf("host = %q, want %q", got.host, tt.want.host)
			}
			if got.remoteSock != tt.want.remoteSock {
				t.Errorf("remoteSock = %q, want %q", got.remoteSock, tt.want.remoteSock)
			}
			if got.sshOpts != tt.want.sshOpts {
				t.Errorf("sshOpts = %+v, want %+v", got.sshOpts, tt.want.sshOpts)
			}
		})
	}
}
