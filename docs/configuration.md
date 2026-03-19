# Configuration

## Agent config

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

## Alert reference

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

## Client config

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
