# tori ─(•)>

[![Go Report Card](https://goreportcard.com/badge/github.com/thobiasn/tori-cli)](https://goreportcard.com/report/github.com/thobiasn/tori-cli)
[![Coverage (core)](https://codecov.io/gh/thobiasn/tori-cli/branch/main/graph/badge.svg?flag=core)](https://codecov.io/gh/thobiasn/tori-cli)
[![GitHub Release](https://img.shields.io/github/v/release/thobiasn/tori-cli)](https://github.com/thobiasn/tori-cli/releases)
[![License](https://img.shields.io/github/license/thobiasn/tori-cli)](LICENSE)

Docker monitoring that fits in an SSH connection.

Most Docker VPS setups don't need a full monitoring stack. Seeing your monitoring tool use more resources than the thing it's watching is painful when all you really need is just to be alerted when something breaks at 3am, or find that one log line during debugging.

tori is that tool. Single binary, SSH-only, no dashboards, no stack.

![tori demo](https://github.com/user-attachments/assets/e3b8c171-f57b-4b1e-99bb-d2541b0125b0)

## When tori is a good fit

- You run Docker on 1–10 servers
- You care deeply about attack surface
- You don’t want to maintain a big stack like Prometheus + Grafana
- You want alerts when something goes wrong
- You prefer terminal tools over dashboards

## Quick Start

### On your server

```bash
curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sudo sh
```

Edit `/etc/tori/config.toml` to get notified when something breaks:

```toml
[docker]
# include = ["myapp-*"]    # auto-track containers matching a pattern
# include = ["*"]          # auto-track all containers (use cautiously)

[alerts.container_down]
condition = "container.state == 'exited'"
for = "30s"
severity = "critical"
actions = ["notify"]

# add [notify.email] or [[notify.webhooks]] — see Configuration docs
```

Start the agent:

```bash
sudo systemctl enable --now tori
```

tori is now collecting host metrics. Containers matching the `include` patterns are tracked automatically — the rest are visible in the TUI but need to be tracked manually with `t`.

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

## Features

- **No exposed ports** — all communication over SSH to a Unix socket. No HTTP server, nothing to firewall
- **Single binary, minimal footprint** — one process, typically under 50MB of memory, SQLite for storage. No stack to deploy
- **Alerting** — configurable rules for host metrics, container state, and log patterns. Email and webhook notifications, even when you're not connected
- Host metrics — CPU, memory, disk, network, swap, load averages
- Docker container monitoring — status, stats, health checks, restart tracking
- Log tailing with regex search, level filtering, match highlighting, and date/time range filters
- Multi-server support — monitor multiple hosts from one terminal, switch instantly

## Contents

- [Installation](#installation)
- [Usage Notes](#usage-notes)
- [Requirements](#requirements)
- [Configuration](docs/configuration.md) — agent config, alert reference, client config, theming
- [Connecting](docs/how-it-works.md#connecting) — CLI options, multi-server, SSH tunnels
- [Security](docs/how-it-works.md#security) — socket permissions, Docker access, encryption
- [How It Works](docs/how-it-works.md) — architecture, storage, troubleshooting
- [Keybindings](docs/keybindings.md) — keyboard reference

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

## Usage Notes

> [!TIP]
> All containers are visible in the TUI by default, but **tracking is opt-in**. Only tracked containers get metrics history, log storage, and alert evaluation. Press `t` to toggle tracking, or set `include` patterns in the agent config for automatic tracking.

> [!NOTE]
> **Storage:** High-volume containers can grow the SQLite database significantly. Reduce `retention_days` (default: 7) or be selective about which containers you track.

> [!NOTE]
> **Log alert windows** must be shorter than `retention_days` — pruned logs can't be counted. Keep windows short (minutes to hours) for responsive alerting.

## Requirements

- Linux (the agent reads from `/proc` and `/sys`)
- Docker (for container monitoring)
- SSH access to the server (for remote connections)
- Go 1.25+ (build from source only)

---

Built by [thobiasn](https://thobiasn.dev)
