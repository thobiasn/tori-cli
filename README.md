# tori ─(•)>

Lightweight server monitoring for Docker environments. A single binary that replaces the Grafana+Prometheus+Loki stack when all you need is a terminal.

![tori demo](https://github.com/user-attachments/assets/869f8dad-3f3b-4910-a5a1-069c5e482da0)

<details>
<summary>Disclaimer</summary>

This project is still very much in development and being tested. I was frustrated that I couldn't find the monitoring/alerting solution that was just right for small scale hosting on a single or multiple VPSs so I decided to build the thing that was just right for my needs. What I'm saying is that you should use this at your own risk, don't expect all features to work yet. Feel free to open issues if you encounter something but don't expect a professional team to solve your specific needs.

</details>

## Features

- Host metrics — CPU, memory, disk, network, swap, load averages
- Docker container monitoring — status, stats, health checks, restart tracking
- Container log tailing with filtering by container, compose group, text search, date/time and stream
- Alerting with configurable rules, email (SMTP), and webhook notifications
- SQLite storage with configurable retention
- Multi-server support — monitor multiple hosts from one terminal
- Single binary, zero runtime dependencies
- No exposed ports — all communication over SSH

## How It Works

tori has two parts. The **agent** runs on your server collecting metrics, tailing logs, and evaluating alerts 24/7. The **client** runs on your machine and connects through an SSH tunnel to a Unix socket — no HTTP server, no open ports.

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

## Quick Start

### On your server

```bash
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sudo sh
sudo systemctl enable --now tori
```

### On your machine

```bash
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sh -s -- --client
```

Add a server to `~/.config/tori/config.toml`:

```toml
[servers.prod]
host = "user@prod.example.com"
```

Connect:

```bash
tori
```

That's it — tori connects over SSH, no extra ports or setup needed.

## Installation

### Step 1 — Agent (server)

The agent runs on Linux only (it reads from `/proc` and `/sys`).

<details>
<summary><b>Linux (systemd)</b></summary>

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

When running via Docker, set the host paths in your config to the mounted locations:

```toml
[host]
proc = "/host/proc"
sys = "/host/sys"
```

You can also inject the entire config via the `TORI_CONFIG` environment variable instead of mounting a file. This is useful for PaaS platforms like Dokploy or Coolify where you don't have easy access to the host filesystem — see `deploy/docker-compose.yml` for an example.

</details>

<details>
<summary><b>From source</b></summary>

```bash
go build -o tori ./cmd/tori
sudo ./tori agent --config /etc/tori/config.toml
```

</details>

### Step 2 — Client (your machine)

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

## Connecting

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

Numeric fields support `>`, `<`, `>=`, `<=`, `==`, `!=`. String fields support `==` and `!=` only, with values in single quotes.

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
```

The `[display]` section controls how timestamps appear in logs and alerts. Both fields use [Go time layout](https://pkg.go.dev/time#pkg-constants) strings:

| Style | `date_format` | `time_format` |
|---|---|---|
| ISO 24h (default) | `2006-01-02` | `15:04:05` |
| US 12h | `01/02` | `3:04PM` |
| European short | `02 Jan` | `15:04` |

## Keybindings

### Global

| Key | Action |
|-----|--------|
| `1` | Dashboard view |
| `2` | Alerts view |
| `+`/`-` | Zoom time window |
| `S` | Switch server |
| `?` | Help |
| `q` | Quit |

### Dashboard

| Key | Action |
|-----|--------|
| `j`/`k` | Navigate containers |
| `Enter` | Open detail view |
| `Space` | Expand/collapse compose group |
| `t` | Toggle tracking for container/group |

### Alerts

| Key | Action |
|-----|--------|
| `Tab` | Switch focus between alerts and rules |
| `j`/`k` | Navigate up/down |
| `Enter` | Expand details |
| `a` | Acknowledge alert |
| `s` | Silence rule |
| `r` | Show/hide resolved alerts |
| `g` | Go to container |

### Detail View (Logs + Metrics)

| Key | Action |
|-----|--------|
| `j`/`k` | Scroll logs |
| `G` | Jump to latest |
| `Enter` | Expand log entry |
| `s` | Cycle stream filter (all/stdout/stderr) |
| `f` | Open filter dialog |
| `i` | Toggle info overlay |
| `Esc` | Back to dashboard |

## Updating

Re-run the same install command to update to the latest version. Existing configs are preserved.

```bash
# Agent (then restart the service)
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sudo sh
sudo systemctl restart tori

# Client
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sh -s -- --client
```

## Uninstall

```bash
sudo systemctl disable --now tori
sudo rm /usr/local/bin/tori
sudo rm /etc/systemd/system/tori.service
sudo rm -rf /etc/tori /var/lib/tori /run/tori
sudo userdel tori
```

For client-only installs, just remove the binary (`~/.local/bin/tori` or `/usr/local/bin/tori`) and config (`~/.config/tori/`).

## Security

**Docker socket access:** tori requires read-only access to the Docker socket (`/var/run/docker.sock`) for container monitoring. This is the same trust model as lazydocker, ctop, and other Docker monitoring tools. The socket is always mounted `:ro` — tori never writes to Docker.

**Unix socket permissions:** The tori socket at `/run/tori/tori.sock` is the only way to interact with the agent. The default file mode is `0666` because SSH is the real auth gate — anyone who can reach the socket already has shell access to the server. tori doesn't expand the attack surface.

**Config file:** The agent config contains SMTP credentials and webhook URLs. Permissions should be `0600` owned by the user running the agent.

**No exposed ports:** tori does not listen on any network port. All client communication goes through SSH to the Unix socket. There is no HTTP server, no API endpoint, nothing to expose or firewall. SSH compression is enabled by default on all tunnels to reduce bandwidth for metrics and log traffic.

**Log contents:** tori stores container logs in SQLite. These may contain sensitive application data (tokens, user info, errors with PII). The database file at `/var/lib/tori/tori.db` should have restrictive permissions and the retention policy should be set appropriately.

## Requirements

- Linux (the agent reads from `/proc` and `/sys`)
- Docker (for container monitoring)
- SSH access to the server (for remote connections)
- Go 1.25+ (build from source only)
