# Tori ─(•)>

Lightweight server monitoring for Docker environments. A single binary that replaces the Grafana+Prometheus+Loki stack when all you need is a terminal.

![Tori demo](https://github.com/user-attachments/assets/35df5fe1-e61a-4ccc-be34-a31c7a0ea6aa)

This project is still very much in development and being tested. I was frustrated that I couldn't find the monitoring/alerting solution that was just right for small scale hosting on a single or multiple VPSs so I decided to build the thing that was just right for my needs. What I'm saying is that you should use this at your own risk, feel free to open issues if you encounter something but don't expect a professional team to solve your specific needs.

## Features

- Host metrics — CPU, memory, disk, network, swap, load averages
- Docker container monitoring — status, stats, health checks, restart tracking
- Container log tailing with filtering by container, compose group, stream, and text search
- Alerting with configurable rules, email (SMTP), and webhook notifications
- SQLite storage with configurable retention
- Multi-server support — monitor multiple hosts from one terminal
- Single binary, zero runtime dependencies
- No exposed ports — all communication over SSH

## How It Works

The agent runs on your server collecting metrics and evaluating alerts 24/7. The TUI client connects from your local machine through an SSH tunnel to a Unix socket — no HTTP server, no open ports.

```
┌─────────────────────────────────────────────┐
│  Your Machine                               │
│  ┌───────────────────────────────────────┐  │
│  │  tori user@host                       │  │
│  │  TUI Client (Bubbletea)               │  │
│  └──────────────┬────────────────────────┘  │
└─────────────────┼───────────────────────────┘
                  │ SSH tunnel
┌─────────────────┼───────────────────────────┐
│  Server         │                           │
│  ┌──────────────▼────────────────────────┐  │
│  │  tori agent                           │  │
│  │  Unix socket: /run/tori/tori.sock     │  │
│  │                                       │  │
│  │  ├── Collector (host metrics, docker) │  │
│  │  ├── Log tailer (docker log API)      │  │
│  │  ├── Alert evaluator                  │  │
│  │  ├── Notifier (email/webhook)         │  │
│  │  └── Storage (SQLite)                 │  │
│  └───────────────────────────────────────┘  │
│       │                │                    │
│       ▼                ▼                    │
│  Docker socket    /proc, /sys               │
└─────────────────────────────────────────────┘
```

## Quick Start

**1. Install the agent on your server:**

```bash
# Binary install
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sudo sh
# edit /etc/tori/config.toml
sudo systemctl enable --now tori

# Or with Docker Compose — copy deploy/docker-compose.yml then:
docker compose up -d
```

**2. Install the client on your machine:**

```bash
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sh -s -- --client
```

**3. Connect:**

```bash
tori user@your-server

# or edit ~/.config/tori/config.toml and just
tori
```

## Updating

Re-run the same install command to update to the latest version. Existing configs are preserved.

```bash
# Agent (then restart the service)
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sudo sh
sudo systemctl restart tori

# Client
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sh -s -- --client
```

## Agent Configuration

The agent config lives at `/etc/tori/config.toml`. All fields have sensible defaults — an empty config file works out of the box.

```toml
# /etc/tori/config.toml

[storage]
path = "/var/lib/tori/tori.db"
retention_days = 7

[socket]
path = "/run/tori/tori.sock"

[host]
proc = "/proc"
sys = "/sys"

[docker]
socket = "/var/run/docker.sock"
# track all containers by default
# can be toggled per-container or per-group at runtime via the TUI
# these filters set the initial state:
# include = ["myapp-*"]
# exclude = ["tori-*"]

[collect]
interval = "10s"

[alerts.container_down]
condition = "container.state == 'exited'"
for = "30s"
severity = "critical"
actions = ["notify"]

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

# [alerts.high_load]
# condition = "host.load1 > 4"     # tune threshold to your CPU count
# for = "5m"
# severity = "warning"
# actions = ["notify"]

[alerts.high_swap]
condition = "host.swap_percent > 80"
for = "2m"
severity = "warning"
actions = ["notify"]

[alerts.container_memory]
condition = "container.memory_percent > 90"
for = "1m"
severity = "warning"
actions = ["notify"]

[alerts.unhealthy]
condition = "container.health == 'unhealthy'"
for = "30s"
severity = "critical"
actions = ["notify"]

[alerts.restart_loop]
condition = "container.restart_count > 5"
for = "0s"
severity = "critical"
actions = ["notify"]

[alerts.bad_exit]
condition = "container.exit_code != 0"
for = "0s"
severity = "warning"
actions = ["notify"]

[notify.email]
enabled = true
smtp_host = "smtp.example.com"
smtp_port = 587
from = "tori@example.com"
to = ["you@example.com"]

[[notify.webhooks]]
enabled = true
url = "https://hooks.slack.com/services/..."
# headers = { Authorization = "Bearer token" }
# template = '{"text": "{{.Subject}}\n{{.Body}}"}'
```

Alert conditions use the format `scope.field op value`. Available fields:

| Field | Type | Description |
|---|---|---|
| `host.cpu_percent` | numeric | CPU usage percentage |
| `host.memory_percent` | numeric | Memory usage percentage |
| `host.disk_percent` | numeric | Disk usage percentage (per-mountpoint) |
| `host.load1` | numeric | 1-minute load average |
| `host.load5` | numeric | 5-minute load average |
| `host.load15` | numeric | 15-minute load average |
| `host.swap_percent` | numeric | Swap usage percentage |
| `container.cpu_percent` | numeric | Container CPU usage |
| `container.memory_percent` | numeric | Container memory usage |
| `container.state` | string | Container state (e.g. `'running'`, `'exited'`) |
| `container.health` | string | Container health (e.g. `'healthy'`, `'unhealthy'`) |
| `container.restart_count` | numeric | Container restart count |
| `container.exit_code` | numeric | Container exit code |

Numeric fields support `>`, `<`, `>=`, `<=`, `==`, `!=`. String fields support `==` and `!=` only, with values in single quotes.

## Client Configuration

```toml
# ~/.config/tori/config.toml

[servers.prod]
host = "user@prod.example.com"
socket = "/run/tori/tori.sock"
# port = 2222                          # custom SSH port (default: 22)
# identity_file = "~/.ssh/prod_key"    # path to SSH private key
# auto_connect = true                  # connect on startup (default: false)

[servers.staging]
host = "user@staging.example.com"
socket = "/run/tori/tori.sock"

[display]
# date_format = "2006-01-02"           # Go time layout (default: ISO date)
# time_format = "15:04:05"             # Go time layout (default: 24h clock)
```

The `[display]` section controls how timestamps appear in logs and alerts. Both fields use [Go time layout](https://pkg.go.dev/time#pkg-constants) strings:

| Style | `date_format` | `time_format` |
|---|---|---|
| ISO 24h (default) | `2006-01-02` | `15:04:05` |
| US 12h | `01/02` | `3:04PM` |
| European short | `02 Jan` | `15:04` |

## Installation

### Agent (server)

The install script downloads the latest release, creates a `tori` system user, sets up directories, and installs a systemd service:

```bash
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sudo sh
```

To install a specific version:

```bash
sudo sh install.sh --version v1.0.0
```

After installation:

```bash
sudo vim /etc/tori/config.toml        # configure alerts, notifications
sudo systemctl enable --now tori       # start the agent
systemctl status tori                  # check it's running
journalctl -u tori -f                  # follow agent logs
sudo systemctl reload tori             # reload config without restart (SIGHUP)
```

### Client (your machine)

Install just the client binary — no root required, works on Linux and macOS (Windows via WSL):

```bash
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sh -s -- --client
```

Installs to `~/.local/bin/tori` (or `/usr/local/bin/tori` if run as root).

### Docker

```bash
docker pull ghcr.io/thobiasn/tori-cli:latest
```

Run with a config file on the host:

```bash
docker run -d --name tori \
  --restart unless-stopped \
  --pid host \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v /proc:/host/proc:ro \
  -v /sys:/host/sys:ro \
  -v /run/tori:/run/tori \
  -v tori-data:/var/lib/tori \
  -v ./config.toml:/etc/tori/config.toml:ro \
  ghcr.io/thobiasn/tori-cli:latest
```

When running via Docker, set the host paths in your config to the mounted locations:

```toml
[host]
proc = "/host/proc"
sys = "/host/sys"
```

Or inject the entire config via the `TORI_CONFIG` environment variable (useful for PaaS platforms like Dokploy or Coolify where you don't have easy access to the host filesystem):

```bash
docker run -d --name tori \
  --restart unless-stopped \
  --pid host \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v /proc:/host/proc:ro \
  -v /sys:/host/sys:ro \
  -v /run/tori:/run/tori \
  -v tori-data:/var/lib/tori \
  -e TORI_CONFIG='[storage]
path = "/var/lib/tori/tori.db"
[socket]
path = "/run/tori/tori.sock"
[host]
proc = "/host/proc"
sys = "/host/sys"
[docker]
socket = "/var/run/docker.sock"
[collect]
interval = "10s"' \
  ghcr.io/thobiasn/tori-cli:latest
```

A ready-to-use Docker Compose file is provided at `deploy/docker-compose.yml` with `TORI_CONFIG` pre-filled with sensible defaults including alert rules.

To build from source as a Docker image instead:

```bash
docker build -f deploy/Dockerfile -t tori .
```

### From Source

```bash
go build -o tori ./cmd/tori
```

## Connecting

```bash
# Connect to all configured servers
tori

# Connect via SSH
tori user@myserver.com

# With custom SSH port
tori user@host --port 2222

# With specific key
tori user@host --identity ~/.ssh/id_ed25519

# Custom remote socket path
tori user@host --remote-socket /custom/tori.sock

# Direct local socket (no SSH)
tori --socket /run/tori/tori.sock
```

When connected to multiple servers, use `Tab` to focus the servers panel, then `j`/`k` and `Enter` to switch. Each server has isolated data — switching is instant since all sessions receive data concurrently.

## Keybindings

### Global

| Key | Action |
|-----|--------|
| `1` | Dashboard view |
| `2` | Alerts view |
| `+`/`-` | Zoom time window |
| `?` | Help |
| `q` | Quit |

### Dashboard

| Key | Action |
|-----|--------|
| `Tab` | Cycle focus between panels |
| `j`/`k` | Navigate up/down |
| `Enter` | Select / expand |
| `Space` | Collapse/expand compose group |
| `t` | Toggle tracking for container/group |

### Alerts

| Key | Action |
|-----|--------|
| `Tab` | Toggle focus between alerts and rules |
| `j`/`k` | Navigate up/down |
| `Enter` | Expand alert details |
| `a` | Acknowledge alert |
| `s` | Silence alert/rule |
| `r` | Show/hide resolved alerts |
| `g` | Go to container |

### Detail View (Logs + Metrics)

| Key | Action |
|-----|--------|
| `j`/`k` | Scroll logs |
| `Enter` | Expand log entry |
| `s` | Cycle stream filter (all/stdout/stderr) |
| `f` | Open log filter |
| `g` | Cycle project filter |
| `Esc` | Back to dashboard |

## Security

**Docker socket access:** Tori requires read-only access to the Docker socket (`/var/run/docker.sock`) for container monitoring. This is the same trust model as lazydocker, ctop, and other Docker monitoring tools. The socket is always mounted `:ro` — Tori never writes to Docker.

**Unix socket permissions:** The Tori socket at `/run/tori/tori.sock` is the only way to interact with the agent. The default file mode is `0666` because SSH is the real auth gate — anyone who can reach the socket already has shell access to the server. Tori doesn't expand the attack surface.

**Config file:** The agent config contains SMTP credentials and webhook URLs. Permissions should be `0600` owned by the user running the agent.

**No exposed ports:** Tori does not listen on any network port. All client communication goes through SSH to the Unix socket. There is no HTTP server, no API endpoint, nothing to expose or firewall.

**Log contents:** Tori stores container logs in SQLite. These may contain sensitive application data (tokens, user info, errors with PII). The database file at `/var/lib/tori/tori.db` should have restrictive permissions and the retention policy should be set appropriately.

## Requirements

- Linux (the agent reads from `/proc` and `/sys`)
- Docker (for container monitoring)
- SSH access to the server (for remote connections)
- Go 1.25+ (build from source only)
