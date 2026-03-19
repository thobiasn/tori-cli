# How It Works

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

## Security

**Docker socket access:** tori requires read-only access to the Docker socket (`/var/run/docker.sock`) for container monitoring. This is the same trust model as lazydocker, ctop, and other Docker monitoring tools. The socket is always mounted `:ro` — tori never writes to Docker.

**Unix socket permissions:** The tori socket at `/run/tori/tori.sock` defaults to mode `0660`, so only the `tori` group can connect. Add users with `usermod -aG tori <user>` (the install script does this for `$SUDO_USER`). Docker compose deployments set `mode = "0666"` explicitly because the container runs as root and can't manage host groups.

**Config file:** The agent config contains SMTP credentials and webhook URLs. Permissions should be `0600` owned by the user running the agent.

**No exposed ports:** tori does not listen on any network port. All client communication goes through SSH to the Unix socket. There is no HTTP server, no API endpoint, nothing to expose or firewall. SSH compression is enabled by default on all tunnels to reduce bandwidth for metrics and log traffic.

**Log contents:** tori stores container logs in SQLite. These may contain sensitive application data (tokens, user info, errors with PII). The database file at `/var/lib/tori/tori.db` should have restrictive permissions and the retention policy should be set appropriately.

## Storage

All logs from tracked containers are stored in SQLite for the full `retention_days` window (default: 7 days). High-volume containers can grow the database significantly. If storage is a concern, reduce `retention_days` in the agent config. You can also be selective about which containers you track — the `t` key in the dashboard toggles tracking per-container, and only tracked containers have their logs stored.

Log alert `window` values must be shorter than your `retention_days` — logs outside the retention window have been pruned and can't be counted. In practice, keep windows short (minutes to hours) for responsive alerting.

## Troubleshooting

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
