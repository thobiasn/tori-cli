# tori ─(•)>

[![Go Report Card](https://goreportcard.com/badge/github.com/thobiasn/tori-cli)](https://goreportcard.com/report/github.com/thobiasn/tori-cli)
[![Coverage (core)](https://codecov.io/gh/thobiasn/tori-cli/branch/main/graph/badge.svg?flag=core)](https://codecov.io/gh/thobiasn/tori-cli)
[![GitHub Release](https://img.shields.io/github/v/release/thobiasn/tori-cli)](https://github.com/thobiasn/tori-cli/releases)
[![License](https://img.shields.io/github/license/thobiasn/tori-cli)](LICENSE)

Docker server monitoring without the stack. Metrics, logs, and alerts from your terminal. Single binary, minimal footprint, zero exposed ports, SSH-only.

If you're running Docker and a full monitoring stack feels like overkill, tori gives you metrics, logs, and alerts in a single binary that uses less memory than most of the containers it watches. Install on your server, add a notification channel, and you're covered. tori watches your containers 24/7 and alerts you when something breaks, even when you're not connected. When you want to look, everything is right there in your terminal.

![tori demo](https://github.com/user-attachments/assets/e0183dc7-9b0e-4358-a7ed-dfd508b2e5e0)

## Table of Contents

- [Features](#features)
- [Quick Start](#quick-start)
- [How It Works](#how-it-works)
- [Installation](#installation)
  - [Agent (server)](#agent-server)
  - [Client (your machine)](#client-your-machine)
  - [Connecting](#connecting)
  - [Updating](#updating)
  - [Uninstalling](#uninstalling)
- [Configuration](#configuration)
  - [Agent config](#agent-config)
  - [Alert reference](#alert-reference)
  - [Client config](#client-config)
- [Keybindings](#keybindings)
- [Usage Notes](#usage-notes)
  - [Security](#security)
  - [Storage](#storage)
  - [Troubleshooting](#troubleshooting)
- [Requirements](#requirements)

## Features

- **No exposed ports** — all communication over SSH to a Unix socket. No HTTP server, nothing to firewall
- **Single binary, minimal footprint** — one process, typically under 50MB of memory, SQLite for storage. No stack to deploy
- **Alerting** — configurable rules for host metrics, container state, and log patterns. Email and webhook notifications, even when you're not connected
- Host metrics — CPU, memory, disk, network, swap, load averages
- Docker container monitoring — status, stats, health checks, restart tracking
- Log tailing with regex search, level filtering, match highlighting, and date/time range filters
- Multi-server support — monitor multiple hosts from one terminal, switch instantly

## Quick Start

### On your server

```bash
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sudo sh
```

Edit `/etc/tori/config.toml` to get notified when something breaks:

```toml
[docker]
# include = ["myapp-*"]    # auto-track containers matching a pattern
# include = ["*"]          # auto-track all containers

[alerts.container_down]
condition = "container.state == 'exited'"
for = "30s"
severity = "critical"
actions = ["notify"]

# add [notify.email] or [[notify.webhooks]] — see Configuration below
```

Start the agent:

```bash
sudo systemctl enable --now tori
```

tori is now collecting host metrics and watching all your containers.

### On your machine

```bash
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sh -s -- --client
tori user@your-server.com
```

Or add servers to `~/.config/tori/config.toml` for persistent config:

```toml
[servers.prod]
host = "user@prod.example.com"
```

> [!TIP]
> All containers are visible by default, but tracking is opt-in. Press `t` on any container or compose group to track it — this enables metrics history, log storage, and alert evaluation. Tracking persists across agent restarts. For automatic tracking, set `include` patterns in the agent config (e.g. `include = ["myapp-*"]`).

## How It Works

tori has two parts. The **agent** runs on your server collecting metrics, tailing logs, and evaluating alerts 24/7. The **client** runs on your machine and connects to the remote agent through an SSH tunnel to a Unix socket — no HTTP server, no open ports.

```
┌─────────────────────────────────────────────┐
│  Your Machine                               │
│  ┌───────────────────────────────────────┐  │
│  │  tori                                 │  │
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

## Installation

### Agent (server)

The agent runs on Linux only (it reads from `/proc` and `/sys`).

<details>
<summary><b>Linux (systemd)</b></summary>

The install script downloads the latest release, creates a `tori` system user, sets up directories, and installs a systemd service:

```bash
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sudo sh
```

To install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sudo sh -s -- --version v1.0.0
```

After installation:

```bash
# configure alerts, notifications
sudo vim /etc/tori/config.toml

# start the agent
sudo systemctl enable --now tori

# check it's running
systemctl status tori

# follow agent logs
journalctl -u tori -f

# reload config without restart (SIGHUP)
sudo systemctl reload tori
```

</details>

<details>
<summary><b>Arch Linux (AUR)</b></summary>

```bash
yay -S tori-cli-bin
```

Installs the binary, systemd service, and creates the tori user and directories.

</details>

<details>
<summary><b>Docker Compose</b></summary>

A ready-to-use Docker Compose file is provided at [`deploy/docker-compose.yml`](deploy/docker-compose.yml) with sensible defaults including alert rules:

```bash
curl -O https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/docker-compose.yml
# edit the TORI_CONFIG section to configure alerts, notifications
docker compose up -d
```

</details>

<details>
<summary><b>Docker run</b></summary>

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

When running via Docker, set the host paths and socket mode in your config:

```toml
[socket]
mode = "0666"    # required for Docker — allows host users to reach the socket

[host]
proc = "/host/proc"
sys = "/host/sys"
```

The socket is volume-mounted to the host at `/run/tori`, so SSH remains the auth gate — not file permissions.

You can also inject the entire config via the `TORI_CONFIG` environment variable instead of mounting a file. This is useful for PaaS platforms like Dokploy or Coolify where you don't have easy access to the host filesystem — see `deploy/docker-compose.yml` for an example.

</details>

<details>
<summary><b>From source</b></summary>

```bash
go build -o tori ./cmd/tori
sudo ./tori agent --config /etc/tori/config.toml
```

</details>

### Client (your machine)

<details>
<summary><b>Linux</b></summary>

```bash
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sh -s -- --client
```

Installs to `~/.local/bin/tori` (or `/usr/local/bin/tori` if run as root).

</details>

<details>
<summary><b>macOS</b></summary>

```bash
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sh -s -- --client
```

Installs to `~/.local/bin/tori` (or `/usr/local/bin/tori` if run with sudo).

</details>

<details>
<summary><b>Windows (WSL)</b></summary>

Install [WSL](https://learn.microsoft.com/en-us/windows/wsl/install), then follow the Linux instructions above.

</details>

<details>
<summary><b>From source</b></summary>

```bash
go build -o tori ./cmd/tori
```

</details>

### Connecting

```bash
# Connect to all configured servers
tori

# Connect via SSH (no config needed)
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

When connected to multiple servers, use `S` to open the servers dialog, then `j`/`k` and `Enter` to switch. Each server has isolated data — switching is instant since all sessions receive data concurrently.

### Updating

Re-run the same install command to update to the latest version. Existing configs are preserved.

```bash
# Agent (then restart the service)
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sudo sh
sudo systemctl restart tori

# Client
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sh -s -- --client
```

### Uninstalling

```bash
sudo systemctl disable --now tori
sudo rm /usr/local/bin/tori
sudo rm /etc/systemd/system/tori.service
sudo rm -rf /etc/tori /var/lib/tori /run/tori
sudo userdel tori
sudo groupdel tori 2>/dev/null   # may remain if other users were added to it
```

For client-only installs, just remove the binary (`~/.local/bin/tori` or `/usr/local/bin/tori`) and config (`~/.config/tori/`).

## Configuration

### Agent config

The agent config lives at `/etc/tori/config.toml`. All fields have sensible defaults — an empty config file works out of the box.

```toml
# /etc/tori/config.toml

[storage]
path = "/var/lib/tori/tori.db"
retention_days = 7

[socket]
path = "/run/tori/tori.sock"
# mode = "0660"                  # "0660" (default, tori group) or "0666" (compose)

[host]
proc = "/proc"
sys = "/sys"

[docker]
socket = "/var/run/docker.sock"
# include = ["myapp-*"]    # auto-track containers matching these patterns (use ["*"] for all)
# exclude = ["tori-*"]     # never auto-track containers matching these patterns

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

[alerts.error_spike]
condition = "log.count > 10"
match = "error"
window = "5m"
severity = "warning"
actions = ["notify"]

[alerts.mem_killed]
condition = "log.count > 0"
match = "\\bOOM\\b|\\bout of memory\\b"
match_regex = true
window = "10m"
severity = "critical"
actions = ["notify"]

[notify.email]
enabled = true
smtp_host = "smtp.example.com"
smtp_port = 587
from = "tori@example.com"
to = ["you@example.com"]
username = "tori@example.com"
password = "app-password-here"
tls = "starttls"

[[notify.webhooks]]
enabled = true
url = "https://hooks.slack.com/services/..."
# headers = { Authorization = "Bearer token" }
# template = '{"text": "{{.Subject}}\n{{.Body}}\nSeverity: {{.Severity}} Status: {{.Status}}"}'
```

Webhook template fields: `{{.Subject}}`, `{{.Body}}`, `{{.Severity}}` (warning/critical), `{{.Status}}` (firing/resolved/test). All values are automatically JSON-escaped when using a custom template.

**Email TLS modes:** `starttls` (port 587, upgrades to TLS after connect), `tls` (port 465, implicit TLS), or omit for local relay (no encryption). Authentication (`username`/`password`) requires TLS.

### Alert reference

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
| `container.cpu_percent` | numeric | Container CPU usage (100% = 1 core) |
| `container.cpu_limit_percent` | numeric | CPU usage as percentage of configured limit (0 if no limit) |
| `container.memory_percent` | numeric | Container memory usage (% of limit, or % of host total if no limit) |
| `container.state` | string | Container state (e.g. `'running'`, `'exited'`) |
| `container.health` | string | Container health (e.g. `'healthy'`, `'unhealthy'`) |
| `container.restart_count` | numeric | Container restart count |
| `container.exit_code` | numeric | Container exit code |
| `log.count` | numeric | Number of log lines matching `match` within `window` (per-container) |

Numeric fields support `>`, `<`, `>=`, `<=`, `==`, `!=`. String fields support `==` and `!=` only, with values in single quotes.

Log rules require two additional fields:

| Field | Description |
|---|---|
| `match` | Text pattern or regex to match against log messages |
| `window` | Time window for counting matches (e.g. `"5m"`, `"1h"`) |
| `match_regex` | Set to `true` for regex matching (default: `false`, uses substring match) |

Log rules are container-scoped — each tracked container is evaluated independently. Matching is case-insensitive. Only tracked containers with log collection enabled will be evaluated.

Each alert rule supports these optional timing fields:

| Field | Default | Description |
|---|---|---|
| `for` | `0s` | Condition must stay true for this duration before firing |
| `cooldown` | `5m` | After resolution, wait this long before the same instance can re-fire (prevents flapping) |
| `notify_cooldown` | `5m` | After notifying for a rule, suppress duplicate notifications for this duration (per-rule, not per-instance) |

Set any of these to `"0s"` to disable.

### Client config

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

# [theme]
# Colors default to ANSI (0-15) so the TUI inherits your terminal theme.
# Override with ANSI numbers, 256-palette numbers, or hex values.
# accent = "4"                         # ANSI blue
# critical = "#f7768e"                 # hex override
```

The `[display]` section controls how timestamps appear in logs and alerts. Both fields use [Go time layout](https://pkg.go.dev/time#pkg-constants) strings:

| Style | `date_format` | `time_format` |
|---|---|---|
| ISO 24h (default) | `2006-01-02` | `15:04:05` |
| US 12h | `01/02` | `3:04PM` |
| European short | `02 Jan` | `15:04` |

The `[theme]` section overrides individual TUI colors. By default all colors use ANSI values (0–15) so the interface inherits your terminal's color scheme. Any field left unset keeps its ANSI default. Values can be ANSI numbers (`"1"`–`"15"`), 256-palette numbers (`"16"`–`"255"`), or hex (`"#rrggbb"`).

| Field | Default | Purpose |
|---|---|---|
| `fg` | `7` | Default text |
| `fg_dim` | `8` | De-emphasized text (labels, hints) |
| `fg_bright` | `15` | Emphasized text (values, names) |
| `border` | `8` | Dividers, separators |
| `accent` | `4` | Focus indicators, selection |
| `healthy` | `2` | Running, all clear |
| `warning` | `3` | High usage, degraded |
| `critical` | `1` | Exited, unhealthy |
| `debug_level` | `8` | Log level: DEBUG |
| `info_level` | `7` | Log level: INFO |
| `graph_cpu` | `12` | CPU sparkline |
| `graph_mem` | `13` | Memory sparkline |

Because tori defaults to ANSI colors, it automatically inherits your terminal's color scheme:

<img src="https://github.com/user-attachments/assets/59fdca1f-7d3e-489f-9028-076ef3385e23" alt="Tokyo Night" width="350"> <img src="https://github.com/user-attachments/assets/64054c43-b563-4199-9cc6-6ef86554692e" alt="Rosé Pine" width="350"> <img src="https://github.com/user-attachments/assets/ac245a78-48b7-4288-be21-230314ccf748" alt="Osaka Jade" width="350">

## Keybindings

### Navigation (all views)

| Key | Action |
|-----|--------|
| `j`/`k` | Up/down |
| `gg`/`G` | Jump to top/bottom |
| `Ctrl+d`/`Ctrl+u` | Half-page down/up |
| `1`/`2` | Switch to dashboard/alerts view |
| `+`/`-` | Zoom time window |
| `S` | Switch server |
| `y` | Yank to clipboard |
| `Esc` | Back / clear filter |
| `?` | Help |
| `q` | Quit |

### Dashboard

| Key | Action |
|-----|--------|
| `{`/`}` | Jump to previous/next project group |
| `Enter` | Open detail view |
| `Space` | Expand/collapse compose group |
| `t` | Toggle tracking for container/group |

### Alerts

| Key | Action |
|-----|--------|
| `Tab` | Switch focus between alerts and rules |
| `Enter` | Expand details |
| `a` | Acknowledge alert |
| `s` | Silence rule |
| `t` | Test notification (rules section/dialog) |
| `r` | Show/hide resolved alerts |
| `gd` | Go to container |

### Detail View (Logs + Metrics)

| Key | Action |
|-----|--------|
| `Enter` | Expand log entry |
| `s` | Cycle log level filter (ERR → WARN → INFO → DBUG → all) |
| `/` | Open filter dialog (regex search, date/time range) |
| `i` | Toggle info overlay |

## Usage Notes

### Security

**Docker socket access:** tori requires read-only access to the Docker socket (`/var/run/docker.sock`) for container monitoring. This is the same trust model as lazydocker, ctop, and other Docker monitoring tools. The socket is always mounted `:ro` — tori never writes to Docker.

**Unix socket permissions:** The tori socket at `/run/tori/tori.sock` defaults to mode `0660`, so only the `tori` group can connect. Add users with `usermod -aG tori <user>` (the install script does this for `$SUDO_USER`). Docker compose deployments set `mode = "0666"` explicitly because the container runs as root and can't manage host groups.

**Config file:** The agent config contains SMTP credentials and webhook URLs. Permissions should be `0600` owned by the user running the agent.

**No exposed ports:** tori does not listen on any network port. All client communication goes through SSH to the Unix socket. There is no HTTP server, no API endpoint, nothing to expose or firewall. SSH compression is enabled by default on all tunnels to reduce bandwidth for metrics and log traffic.

**Log contents:** tori stores container logs in SQLite. These may contain sensitive application data (tokens, user info, errors with PII). The database file at `/var/lib/tori/tori.db` should have restrictive permissions and the retention policy should be set appropriately.

### Storage

All logs from tracked containers are stored in SQLite for the full `retention_days` window (default: 7 days). High-volume containers can grow the database significantly. If storage is a concern, reduce `retention_days` in the agent config. You can also be selective about which containers you track — the `t` key in the dashboard toggles tracking per-container, and only tracked containers have their logs stored.

Log alert `window` values must be shorter than your `retention_days` — logs outside the retention window have been pruned and can't be counted. In practice, keep windows short (minutes to hours) for responsive alerting.

### Troubleshooting

<details>
<summary><b>SSH connection fails with "connection lost" or "connection closed"</b></summary>

tori connects to remote agents by forwarding a Unix socket over SSH. Some SSH servers (notably Synology DSM and other appliance-style Linux distributions) disable forwarding for non-root users by default.

If local access works (`tori --socket /run/tori/tori.sock` on the server) but remote access fails (`tori user@host`), check the server's `/etc/ssh/sshd_config`. You may need to enable forwarding for your user:

```
Match User YOUR_USER
    AllowTcpForwarding yes
    AllowStreamLocalForwarding yes
```

Restart the SSH service after editing. On Synology, toggle SSH off/on in Control Panel > Terminal & SNMP.

</details>

<details>
<summary><b>Docker: "Bind mount failed: '/run/tori' does not exist"</b></summary>

The `/run/tori` directory must exist on the host before starting the container:

```bash
sudo mkdir -p /run/tori
```

On systems where `/run` is tmpfs (cleared on reboot), add this as a boot task. On Synology, use DSM Task Scheduler with a "Boot-up" triggered task.

</details>

<details>
<summary><b>Docker: "permission denied" connecting to the socket</b></summary>

If the agent runs in Docker, the socket is created as `root` inside the container. The compose file sets `mode = "0666"` to allow any host user to connect. If you're using a custom config, make sure it includes:

```toml
[socket]
mode = "0666"
```

For bare metal installs, add your user to the `tori` group instead: `sudo usermod -aG tori $USER` (then re-login).

</details>

## Requirements

- Linux (the agent reads from `/proc` and `/sys`)
- Docker (for container monitoring)
- SSH access to the server (for remote connections)
- Go 1.25+ (build from source only)

## Author

Built by [thobiasn](https://thobiasn.dev)
