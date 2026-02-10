# TUI Design Reference

Rook's TUI should feel like btop for Docker — information-dense, beautiful, and immediately readable. Every character on screen earns its place. Graphs are the primary visual element, not text.

## Design Principles

Study what makes btop feel "wow":

1. **Graphs dominate.** The first thing you see is moving braille graphs, not text. They make the tool feel alive.
2. **Zero wasted space.** Every panel fills its allocation. No empty voids. If a panel has room, it shows more data.
3. **Visual hierarchy through color.** Your eye is drawn to problems (red) first, then activity (green/cyan), then labels (muted). You can assess system health without reading a single number.
4. **Consistent rhythm.** Labels left-aligned, values right-aligned, fixed-width numbers, same spacing patterns everywhere.
5. **Dense but not cluttered.** Boxes and subtle borders create structure. Data fills the boxes completely.

## Box Drawing

Use Unicode box-drawing characters for all panels. Rounded corners:

```
╭─ Panel Title ──────────────────────────╮
│                                        │
│  content here                          │
│                                        │
╰────────────────────────────────────────╯
```

Characters: `╭ ╮ ╰ ╯ ─ │`
Title is embedded in the top border with a space on each side.
Never use double-line box drawing (`║ ═`).

## Color Palette

All colors are defined in a single `internal/tui/theme.go` file as a `Theme` struct. **No color values anywhere else in the codebase.** Every view imports the theme and references it by name (e.g., `theme.Critical`, `theme.Healthy`). This is what makes custom themes possible later — swap the struct, everything changes.

Use Lipgloss adaptive colors so they work on both light and dark terminals.

### Default theme

```
Critical / Error:    red       (#FF5555 or lipgloss.Color("9"))
Warning:             yellow    (#FFFF55 or lipgloss.Color("11"))
Healthy / OK:        green     (#55FF55 or lipgloss.Color("10"))
Info / Accent:       cyan      (#55FFFF or lipgloss.Color("14"))
Muted / Secondary:   gray      (#888888 or lipgloss.Color("8"))
Label:               white/bold
Value:               default terminal foreground
Background:          default terminal background (never set this explicitly)
```

### Theme struct pattern

```go
type Theme struct {
    Critical lipgloss.Color
    Warning  lipgloss.Color
    Healthy  lipgloss.Color
    Accent   lipgloss.Color
    Muted    lipgloss.Color
    // ... border, label styles etc built from these base colors
}
```

Views never call `lipgloss.Color("9")` directly — they use `theme.Critical`. This is a hard rule.

### Custom themes (future — post-M6)

Users will be able to define themes in a TOML file at `~/.config/rook/theme.toml`. The client loads it at startup and passes the Theme struct to all views. Ship a few built-in themes (default, monokai, nord, solarized) as embedded TOML files. **Do not implement until after M6 — but follow the Theme struct pattern from day one so it's a drop-in addition later.**

**Usage-based color gradient** for metrics (CPU, memory, disk):

- 0–60%: green
- 60–80%: yellow
- 80–100%: red

Apply this to: progress bars, sparkline colors, graph colors, percentage text, container status indicators.

## Graphs and Sparklines

Use **braille characters** (U+2800–U+28FF) for ALL graphs. Each braille character encodes a 2x4 dot grid, giving much higher resolution than block characters. This is what makes btop's graphs look smooth and what creates the "wow" feeling.

**Multi-row braille graphs** for CPU, memory, and container metrics. These are the hero visual element of the dashboard. They should be colored using the usage gradient (green → yellow → red) and fill their panel width completely.

**Single-row sparklines** for inline history in container rows or compact spaces.

**Progress bars** for current usage (disk, memory):

```
MEM  [████████████░░░░░░░░] 58.2%  3.8G / 6.5G
DISK [████████████████░░░░] 81.3%  38G / 47G
```

Use `█` for filled, `░` for empty. Color the filled portion using the usage gradient.

Fall back to block characters (`▁▂▃▄▅▆▇█`) only for simple bar indicators, not for time-series graphs.

## Dashboard Layout

The dashboard is a dense grid of panels. **No empty space.** Every panel uses its full allocation.

```
╭─ CPU ─────────────────────────────────────────╮╭─ Memory ──────────────────────────────────────╮
│ ⣀⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣀⣀⣠⣤⣴⣶⣿⣿⣶⣤⣀⣀⣀⣀⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣀⣀⣀     ││  [██████████████░░░░░░░░░░░░░░]               │
│ ⣀⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣀⣀⣠⣤⣴⣶⣿⣿⣶⣤⣀⣀⣀⣀⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣀⣀⣀     ││  Used: 7.4G / 31.0G  23.8%                    │
│ ⣀⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣀⣀⣠⣤⣴⣶⣿⣿⣶⣤⣀⣀⣀⣀⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣀     4.2% ││  Cached: 15.0G  Free: 9.1G                    │
│ Load: 1.08 1.65 2.24     Uptime: 9d 01h 24m  ││  Swap: 0B / 0B                                │
╰───────────────────────────────────────────────╯╰──────────────────────────────────────────────╯
╭─ Containers ──────────────────────────────────╮╭─ Selected: app-web ── ● running ──────────────╮
│                                               ││ CPU ⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣠⣤⣴⣶⣿⣿⣶⣤⣀   0.3%        │
│  myapp ─────────────────────── 4/4 running    ││     ⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣠⣤⣴⣶⣿⣿⣶⣤⣀              │
│  ● app-web      ✓  0.3%  128M  up 14d  0↻    ││ MEM ⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿   128M / 512M  │
│  ● app-worker   ✓  1.2%   84M  up 14d  0↻    ││     ⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿              │
│  ● postgres     –  0.8%  256M  up 14d  0↻    ││                                               │
│  ● redis        –  0.1%   32M  up 14d  0↻    ││ NET  ▼ 42.1KB/s  ▲ 12.3KB/s                   │
│                                               ││ BLK  ▼  1.2MB/s  ▲  200KB/s                   │
│  backups ─────────────────────── 0/1 running  ││ PID  1284          RESTARTS  0                 │
│  ○ backup-cron  –    –     –   exited   3↻   ││ IMG  myapp:latest                              │
│                                               ││ UP   14d 6h 32m                                │
│  monitoring ──────────────────── 2/2 running  ││ HC   ✓ healthy (last: 2s ago)                  │
│  ● grafana      –  0.4%   92M  up 3d   0↻   ││─────────────────────────────────────────────── │
│  ● prometheus   –  0.6%  180M  up 3d   0↻   ││ DISK [████████░░░░░░░░░░░░] 8.4%  40.1G/474.9G│
│                                               ││ NET  ▼ 20.3KB/s  ▲ 1.0KB/s                    │
╰───────────────────────────────────────────────╯╰────────────────────────────────────────────────╯
╭─ Alerts ── all clear ─────────────────────────────────────────────────────────────────────────╮
╰───────────────────────────────────────────────────────────────────────────────────────────────╯
╭─ Logs ────────────────────────────────────────────────────────────────────────────────────────╮
│  12:04:32 app-web       GET /api/users 200 12ms                                              │
│  12:04:33 app-web       GET /api/users/5 200 8ms                                             │
│  12:04:35 postgres      checkpoint complete: wrote 12 buffers                                │
│  12:04:38 app-worker    processing job queue_id=4821                                         │
│  12:04:41 app-web       POST /api/login 401 3ms                                              │
╰───────────────────────────────────────────────────────────────────────────────────────────────╯
 [1 Dashboard]  2 Logs  3 Alerts  4 Detail │ Space Group  / Filter  ? Help  q Quit
```

### Panel Breakdown

#### Top row: CPU + Memory (equal width, ~6 rows each)

**CPU panel** — the hero graph. Multi-row braille graph showing CPU usage history over time. The graph fills the entire panel width and height minus one line. The bottom line shows: `Load: X.XX X.XX X.XX     Uptime: Xd Xh Xm`. Current CPU % is shown at the right edge of the graph. The graph color follows the usage gradient — if CPU is spiking into the 80s, the graph turns red at those peaks.

**Memory panel** — progress bar at top showing total memory usage. Below it: `Used: X.XG / X.XG  XX.X%`, `Cached: X.XG  Free: X.XG`, `Swap: XG / XG`. If the terminal is wide enough, add a braille sparkline showing memory usage history. This panel is about quick readability of current state, not historical trends (that's what CPU graph does).

#### Middle row: Containers + Selected Detail (equal width, fills remaining space)

**Containers panel (left)** — scrollable list of all containers grouped by compose project. This is the navigation hub. Each container row packs maximum info:

```
● container-name  HC  CPU%  MEM  uptime  restarts
```

Column breakdown:

- `●/○` — state indicator (colored)
- `container-name` — truncated with `…` if needed
- `HC` — health check: `✓` green (healthy), `✗` red (unhealthy), `–` gray (no healthcheck)
- `CPU%` — right-aligned, `0.3%` format
- `MEM` — right-aligned, formatted bytes (`128M`, `1.2G`)
- `uptime` — `up Xd` or `up Xh` or `exited(N)` where N is exit code (muted). Exit codes are valuable: 0 = clean, 1 = error, 137 = OOM killed, 143 = SIGTERM.
- `restarts` — `N↻` where N is restart count. `0↻` is muted, `3↻` is yellow/red

Group headers: `myapp ────────────── 4/4 running` with running/total count. Accent colored. The `────` rule stretches to fill width.

Cursor: highlighted row with subtle background. **Must be clipped to the Containers panel width — never bleed into the adjacent panel.**

Collapsed groups: only show the header line.

**Selected container panel (right)** — shows detailed metrics for whichever container the cursor is on in the Containers panel. Updates instantly as you move the cursor. If no container is selected (cursor is on a group header), show **aggregate metrics for all containers in that group**.

Contents:

- Title: `Selected: container-name ── ● running` (state colored)
- CPU braille graph (2-3 rows) with current % — shows last 5 minutes of history
- MEM braille graph (2-3 rows) with current used / limit
- NET rates: `▼ XX.XKB/s  ▲ XX.XKB/s`
- BLK I/O rates: `▼ XX.XMB/s  ▲ XX.XKB/s`
- PID count, restart count
- Image name, uptime
- Health check status with last check time
- Below a thin separator (`───`): host disk and net summary (always useful context)

When cursor is on a group header, the title changes to `Selected: myapp (4 containers)` and metrics show: combined CPU%, combined MEM, total restarts, running/total count.

#### Alerts panel (full width, dynamic height)

- Active alerts: each on one line with severity icon, time, rule, message, status
- No active alerts: collapses to single-line `╭─ Alerts ── all clear ─╯`
- Active alerts expand the panel, pushing logs down
- Severity colored: CRIT = red `▲`, WARN = yellow `▲`, INFO = cyan `ℹ`

#### Logs panel (full width, bottom, fills remaining space)

- Live-tailing log stream. Takes whatever vertical space is left after alerts.
- Format: `HH:MM:SS  container-name  message` — timestamp muted, name colored per-container, message truncated
- stderr lines get subtle red tint
- Minimum 3 lines visible. If terminal too short, logs panel shrinks first.

### Key interactions on dashboard

- `j/k` or `↑/↓` — move cursor in Containers panel. Selected panel updates instantly.
- `Space` — collapse/expand compose group
- `Enter` — open full-screen detail view for selected container
- `l` — jump to Logs view filtered to selected container
- `Tab` or `1-4` — switch views

### Layout proportions and responsive rules

```
Total height = terminal rows - 1 (footer)

alertH   = max(1, active_alert_count + 2)  // 2 for box borders, 1 if "all clear"
cpuH     = max(6, total * 0.20)            // ~20% of terminal, at least 6 rows
logH     = max(5, remaining * 0.25)         // at least 5 rows, ~25% of remaining
middleH  = total - alertH - logH - cpuH    // containers + selected fill the rest

Width split:
  containers_w = floor(total_w * 0.50)
  selected_w   = total_w - containers_w
  cpu_w        = floor(total_w * 0.50)
  mem_w        = total_w - cpu_w
```

**Responsive breakpoints:**

- Width < 120: hide BLK I/O and PID from Selected panel
- Width < 100: stack Selected below Containers instead of beside. Stack Memory below CPU.
- Width < 80: show "Terminal too narrow" message
- Height < 24: show "Terminal too short" message

**Priority when space is tight:** CPU graph > Containers > Selected > Logs > Alerts detail. Never sacrifice the graph — it's the visual hook.

## Container Status Indicators

```
●  green   — running, healthy
●  yellow  — running, unhealthy or restarting
○  red     — exited, dead, or error
○  gray    — paused, created, or removing
```

Use the filled circle `●` for active states, empty circle `○` for inactive.

## Health Check Indicators

```
✓  green   — healthy (healthcheck passing)
✗  red     — unhealthy (healthcheck failing)
!  yellow  — starting (healthcheck not yet passed)
–  gray    — none (no healthcheck configured)
```

Show in container rows as single character. In detail view, show full status with last check timestamp.

## Text Formatting

- **Labels** are uppercase, muted or bold: `CPU`, `MEM`, `DISK`, `LOAD`, `NET`, `BLK`, `PID`
- **Values** are default foreground, right-aligned within their column
- **Numbers** use fixed-width formatting: `%5.1f%%` for percentages, `%6s` for byte values. This keeps columns aligned as values change.
- **Timestamps** in logs are muted/gray
- **Container names** in logs are colored with a consistent per-container color (fnv32a hash of name mod palette of 6-8 distinct colors)
- **Truncation**: long container names truncate with `…` on the right. Long log lines truncate with `…`. Never wrap inside a panel.
- **Restart count**: `0↻` muted gray, `1-2↻` yellow, `3+↻` red

## Navigation

Consistent across all views:

```
Tab / 1-4        switch between views (dashboard, logs, alerts, detail)
j/k or ↑/↓       move selection up/down in lists
Enter             expand selected item (container detail, full log line, alert detail)
Space             collapse/expand a compose group
l                 jump to logs filtered to selected container
/                 start filtering/searching
q                 quit (with confirmation if connected to multiple servers)
?                 help overlay showing all keybindings
```

Footer bar (muted, bottom of screen):

```
 [1 Dashboard]  2 Logs  3 Alerts  4 Detail │ Space Group  / Filter  ? Help  q Quit
```

Active view is shown with `[brackets]`.

## Log Viewer (Tab 2)

Full-screen view dedicated to log exploration. This is where you go when something is broken and you need to find out why.

```
╭─ Logs ── all containers ── 2,847 lines ── LIVE ─────────────────────────╮
│                                                                          │
│  12:04:32 app-web       GET /api/users 200 12ms                          │
│  12:04:33 app-web       GET /api/users/5 200 8ms                         │
│  12:04:35 postgres      checkpoint complete: wrote 12 buffers            │
│  12:04:38 app-worker    processing job queue_id=4821                     │
│  12:04:41 app-web       POST /api/login 401 3ms                          │
│  12:04:42 app-web       ERROR: invalid token for user_id=291             │
│  12:04:43 app-worker    job failed queue_id=4821 err="timeout"           │
│  12:04:44 redis         1:M 10 Feb 12:04:44.012 * Background saving     │
│                                                                          │
╰──────────────────────────────────────────────────────────────────────────╯
 container: all ▼ │ stream: all ▼ │ / search: _                 │ ? Help
```

### Filtering

Filters are cumulative — combine them to narrow down fast.

**By container:** Press `c` to cycle through containers, or `C` to open a picker. Shows only logs from the selected container. The dashboard container list should also let you press `l` to jump straight to logs filtered to that container.

**By compose group:** Press `g` to filter to all containers in a compose group. Useful when your app spans multiple services and you want to trace a request across them.

**By stream:** Press `s` to toggle between `all`, `stdout`, `stderr`. When debugging, filtering to `stderr` cuts noise fast.

**By text search:** Press `/` to enter search mode. Type a search string — matching lines are highlighted, non-matching lines are dimmed but still visible (not hidden). Press `n`/`N` to jump between matches. Press `Esc` to clear the search.

**By time range:** Press `T` to set a time window. Options: last 5m, 15m, 1h, 6h, 24h, or all (within retention). Defaults to live tail (following new logs as they arrive).

### Behavior

- **Live tail mode** (default): newest logs at the bottom, auto-scrolls as new logs arrive. A `LIVE` indicator shows in the panel title.
- **Scroll up** to pause auto-scroll and enter browse mode. The indicator changes to `PAUSED`. Scroll back down to the bottom to resume live tail.
- **Log lines are syntax-aware:** stderr lines get a subtle red tint. Lines containing common error patterns (`ERROR`, `FATAL`, `panic`, `exception`, stack traces) are highlighted.
- **Press Enter** on a log line to expand it — shows the full untruncated message, useful for JSON logs or stack traces.
- **Press `w`** to toggle line wrapping on/off for the expanded view.

### Log keybindings

```
c / C         filter by container (cycle / picker)
g             filter by compose group
s             toggle stream filter (all / stdout / stderr)
/             text search
n / N         next / previous search match
T             set time range
Enter         expand selected log line
w             toggle wrap in expanded view
Esc           clear filter or search, return to live tail
```

## Container Detail View (Tab 4)

Full-screen detail for a single container. Entered by pressing Enter on a container in the dashboard, or via Tab 4 (shows last-viewed container).

```
╭─ app-web ── ● running ── up 14d 6h 32m ── image: myapp:latest ───────────────────────╮
│                                                                                        │
│  CPU ⣀⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣀⣀⣠⣤⣴⣶⣿⣿⣶⣤⣀⣀⣀⣀⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣀⣀⣀                  0.3%  │
│       ⣀⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣀⣀⣠⣤⣴⣶⣿⣿⣶⣤⣀⣀⣀⣀⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣀⣀⣀                        │
│       ⣀⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣀⣀⣠⣤⣴⣶⣿⣿⣶⣤⣀⣀⣀⣀⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣀⣀⣀                        │
│                                                                                        │
│  MEM ⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿   128M / 512M limit  │
│       ⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿                             │
│       ⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿                             │
│                                                                                        │
│  NET  ▼ 42.1KB/s  ▲ 12.3KB/s          BLK  ▼ 1.2MB/s  ▲ 200KB/s                      │
│  PID  1284            RESTARTS  0      HC   ✓ healthy (last: 2s ago)                   │
│                                                                                        │
╰────────────────────────────────────────────────────────────────────────────────────────╯
╭─ Logs ── app-web ── LIVE ──────────────────────────────────────────────────────────────╮
│  12:04:32 GET /api/users 200 12ms                                                      │
│  12:04:33 GET /api/users/5 200 8ms                                                     │
│  12:04:35 POST /api/login 401 3ms                                                      │
│  12:04:38 ERROR: invalid token for user_id=291                                         │
│  12:04:41 GET /healthz 200 1ms                                                         │
╰────────────────────────────────────────────────────────────────────────────────────────╯
```

The braille graphs for CPU/MEM show the last 5 minutes of history. They fill the available width and use 3 rows each for high-resolution visualization. The graph color follows the usage gradient.

## Responsive Design Rules

- **Minimum terminal size:** 80x24 (standard). Show a message if smaller.
- **Every `View()` function must use the current `tea.WindowSizeMsg` dimensions.** Never hardcode widths or heights.
- **Panel widths are proportional**, not fixed column counts. Calculate from available width.
- **Test at 80x24, 120x40, and 200x60** to catch layout issues at common terminal sizes.

## Things to Avoid

- Don't use emoji. Stick to Unicode symbols (●, ○, ▲, ▼, █, ░, ✓, ✗, ↻, braille).
- Don't use background colors on large text blocks. It looks ugly on most terminals and clashes with user themes. Only use foreground colors. Exception: cursor highlight in lists uses a very subtle background.
- Don't animate or blink anything. Updates happen when new data arrives from the agent, not on a cosmetic timer.
- Don't add decorative borders or ASCII art. Every pixel is functional.
- Don't use more than 6-8 distinct colors total. Too many colors becomes noise.
- Never override the terminal's background color.
- **Don't leave empty space.** If a panel has room, show more data. If it doesn't have room, shrink gracefully.

## Tracking toggle (future — not in initial milestones)

Not all containers need monitoring. Press `t` on a selected container or group to toggle tracking on/off.

- **Tracked** (default): metrics collected, logs tailed, alert rules evaluated
- **Not tracked**: ignored by the agent entirely — no metrics, no logs, no alerts
- Untracked groups show `not tracked` in the section header
- This maps to the include/exclude config on the agent side. Toggling via TUI sends a `action:set_tracking` protocol message that updates the agent's runtime filter.
- **Do not implement until agent, protocol, and TUI are all functional (post-M4).**
