# Rook

A lightweight server monitoring tool for Docker environments. A persistent agent runs on your server collecting metrics, watching containers, tailing logs, and firing alerts 24/7. A TUI client connects to the agent over SSH from your local machine to give you full visibility — no browser, no exposed ports, no extra containers.

## Why

Existing tools either require a full monitoring stack (Grafana + Prometheus + Loki + Alertmanager) or give you a live-only view with no alerting (lazydocker, ctop). Rook is a single binary that replaces both — always-on monitoring with a terminal-native interface.

## Architecture

```
┌─────────────────────────────────────────────┐
│  Your Machine                               │
│  ┌───────────────────────────────────────┐  │
│  │  rook connect user@host               │  │
│  │  TUI Client (Bubbletea)               │  │
│  └──────────────┬────────────────────────┘  │
└─────────────────┼───────────────────────────┘
                  │ SSH tunnel
┌─────────────────┼───────────────────────────┐
│  VPS            │                           │
│  ┌──────────────▼────────────────────────┐  │
│  │  rook agent                           │  │
│  │  Unix socket: /run/rook.sock          │  │
│  │                                       │  │
│  │  ├── Collector (host metrics, docker) │  │
│  │  ├── Log tailer (docker log API)      │  │
│  │  ├── Alert evaluator                  │  │
│  │  ├── Notifier (email/webhook/slack)   │  │
│  │  └── Storage (SQLite)                 │  │
│  └───────────────────────────────────────┘  │
│       │                │                    │
│       ▼                ▼                    │
│  Docker socket    /proc, /sys              │
└─────────────────────────────────────────────┘
```

## Components

**Agent** (`rook agent`) — daemon that runs on the server:

- Collects host metrics from `/proc` and `/sys` (CPU, memory, disk, network)
- Monitors Docker containers via the Docker socket (status, stats, health, restarts)
- Groups containers by Docker Compose project (`com.docker.compose.project` label)
- Per-container and per-group tracking toggle (future) — untracked containers are fully ignored (no metrics, logs, or alerts)
- Tails container logs via the Docker log API
- Evaluates alert rules defined in config and sends notifications (email/SMTP, webhook, Slack)
- Executes self-healing actions (restart container, run command) on alert triggers
- Stores metrics and logs in SQLite with configurable retention
- Exposes a Unix socket for client connections
- Runs as a systemd service or Docker container

**TUI Client** (`rook connect`) — runs on your local machine:

- Connects to the agent via SSH-forwarded Unix socket
- Dashboard view: container status, host metrics, resource usage
- Log viewer: tail and filter logs by container, compose group, stream (stdout/stderr), text search, and time range
- Alert history: view past alerts, acknowledge, silence
- Multi-server: switch between multiple configured servers

## Project Structure

```
rook/
├── cmd/
│   └── rook/               # single binary entry point
│       └── main.go
├── internal/
│   ├── agent/              # collector, alerter, storage, socket server
│   ├── tui/                # bubbletea views and components
│   └── protocol/           # shared message types, msgpack encoding
```

The `internal/protocol` package is the contract between agent and client — all message types, encoding, and socket communication live here. Both `agent` and `tui` import `protocol` but never import each other. This means the binary can be split into separate builds later without any code changes — just add a second entry point under `cmd/`.

## Tech

- **Language:** Go — single static binary, no runtime dependencies
- **TUI:** Bubbletea + Lipgloss + Bubbles (Charm ecosystem)
- **Storage:** SQLite (WAL mode) with configurable retention policies
- **Transport:** SSH tunnel to Unix socket, no extra ports exposed
- **Config:** TOML files
- **Protocol:** msgpack over Unix socket — streaming for live metrics/logs, request-response for historical queries

## Agent Config

```toml
# /etc/rook/config.toml

[storage]
path = "/var/lib/rook/rook.db"
retention_days = 7

[socket]
path = "/run/rook.sock"

[host]
proc = "/proc"
sys = "/sys"

[docker]
socket = "/var/run/docker.sock"
# track all containers by default
# can be toggled per-container or per-group at runtime via the TUI
# these filters set the initial state:
# exclude = ["rook-*"]

[collect]
interval = "10s"

[alerts.container_down]
condition = "container.state == 'exited'"
for = "30s"
severity = "critical"
actions = ["notify", "restart"]
max_restarts = 3

[alerts.high_cpu]
condition = "host.cpu_percent > 90"
for = "2m"
severity = "warning"
actions = ["notify"]

[alerts.high_memory]
condition = "host.memory_percent > 85"
for = "1m"
severity = "warning"
actions = ["notify"]

[alerts.disk_space]
condition = "host.disk_percent > 90"
for = "0s"
severity = "critical"
actions = ["notify"]

[notify.email]
enabled = true
smtp_host = "smtp.example.com"
smtp_port = 587
from = "rook@example.com"
to = ["you@example.com"]

[notify.webhook]
enabled = false
url = "https://hooks.slack.com/services/..."
```

## Client Config

```toml
# ~/.config/rook/config.toml

[servers.prod]
host = "user@prod.example.com"
socket = "/run/rook.sock"

[servers.staging]
host = "user@staging.example.com"
socket = "/run/rook.sock"
```

## Deploy — Binary

```bash
# Install
curl -fsSL https://get.rook.dev | sh

# Start the agent
rook agent --config /etc/rook/config.toml
```

## Deploy — Docker Compose

```yaml
services:
  rook:
    image: ghcr.io/yourname/rook:latest
    command: agent --config /etc/rook/config.toml
    restart: unless-stopped
    pid: host
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro  # remove :ro if using self-healing actions
      - /proc:/host/proc:ro
      - /sys:/host/sys:ro
      - /run/rook:/run/rook
      - rook-data:/var/lib/rook
      - ./config.toml:/etc/rook/config.toml:ro

volumes:
  rook-data:
```

When running via Docker, set the host paths in your config to the mounted locations:

```toml
[host]
proc = "/host/proc"
sys = "/host/sys"
```

The socket is mounted to `/run/rook` on the host so `rook connect` can find it over SSH as usual.

## Connect

```bash
# Connect to a configured server
rook connect prod

# Or connect directly
rook connect user@myserver.com
```

## Security

**Docker socket access:** Rook requires access to the Docker socket (`/var/run/docker.sock`). This is effectively root access to the host — any process that can talk to the Docker daemon can do anything. This is the same trust model as Portainer, LogForge, lazydocker, and every other Docker management tool. In the Docker Compose deployment, mount it read-only (`:ro`) if you don't need self-healing actions (container restart). Remove the `:ro` flag only if you enable self-healing in your alert config.

**Self-healing actions:** When an alert rule includes `actions = ["restart"]`, Rook will restart containers via the Docker API. This means the agent needs write access to the Docker socket. Be deliberate about which alert rules include restart actions and set `max_restarts` to prevent restart loops.

**Unix socket permissions:** The Rook socket at `/run/rook.sock` is the only way to interact with the agent. It should be owned by root and readable only by authorized users. The default file mode is `0660`. Since access is gated by SSH, anyone who can connect already has shell access to the server — Rook doesn't expand the attack surface.

**Config file:** The agent config contains SMTP credentials and webhook URLs. Permissions should be `0600` owned by the user running the agent.

**No exposed ports:** Rook does not listen on any network port. All client communication goes through SSH to the Unix socket. There is no HTTP server, no API endpoint, nothing to expose or firewall.

**Log contents:** Rook stores container logs in SQLite. These may contain sensitive application data (tokens, user info, errors with PII). The database file at `/var/lib/rook/rook.db` should have restrictive permissions and the retention policy should be set appropriately.

## Protocol

The TUI client communicates with the agent over a Unix socket using msgpack-encoded messages.

**Streaming subscriptions** (agent pushes to client):

- `subscribe:metrics` — live host + container metrics
- `subscribe:logs` — live log stream, supports filters (container, compose group, stream, text search)
- `subscribe:alerts` — live alert events
- `subscribe:containers` — real-time container lifecycle events (start, die, destroy, etc.)

**Request-response** (client asks, agent replies):

- `query:metrics` — historical metrics for a time range
- `query:logs` — historical logs with filters (container, compose group, stream, text search, time range)
- `query:alerts` — alert history
- `query:containers` — current container list and status, grouped by compose project
- `action:ack_alert` — acknowledge an alert
- `action:silence_alert` — silence an alert rule for a duration
- `action:restart_container` — manually restart a container
- `action:set_tracking` — (future) enable/disable metric collection, log tailing, and alerting for a container or compose group

## Milestone Plan

**M1 — Agent foundation** (done):
Host metric collection, Docker container discovery and stats, SQLite storage, config loading.

**M2 — Alerting** (done):
Alert rule evaluation, email/SMTP notifications, self-healing actions (container restart), alert persistence.

**M3 — Protocol + socket** (done):
Unix socket server, msgpack protocol, streaming and request-response handlers.

**M4 — TUI client** (done):
SSH tunnel management, dashboard view (containers + host metrics), log viewer with filtering, alert history view, Docker events watcher for real-time container state.

**M5 — Multi-server:**
Client-side server config, server switcher in TUI, concurrent connections.

**M6 — Polish:**
Webhook/Slack notifications, per-container and per-group tracking toggle, config reload without restart, install script.

**Future:**
Custom TUI themes via `~/.config/rook/theme.toml`, built-in theme presets (monokai, nord, solarized).
