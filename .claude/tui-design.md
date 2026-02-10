# TUI Design Reference

Rook's TUI should feel like btop for Docker — information-dense, beautiful, and immediately readable. This document defines the visual language Claude Code should follow when building any TUI view.

## Design Inspiration

btop is the gold standard. Study what makes it work:

- **Boxed panels** with rounded Unicode corners organizing data into clear regions
- **Braille character graphs** (U+2800–U+28FF) for high-resolution sparklines and charts in minimal vertical space
- **Color gradients on usage** — green at low, yellow at moderate, red at high — so you can read system health at a glance without reading numbers
- **Information density** — every character on screen earns its place, but it never feels cluttered because the boxes and whitespace create visual hierarchy
- **Consistent rhythm** — labels left-aligned, values right-aligned, same spacing patterns everywhere

## Box Drawing

Use Unicode box-drawing characters for all panels. Rounded corners, not sharp:

```
╭─ Panel Title ──────────────────────────╮
│                                        │
│  content here                          │
│                                        │
╰────────────────────────────────────────╯
```

Characters:

- Corners: `╭ ╮ ╰ ╯`
- Horizontal: `─`
- Vertical: `│`
- Title is embedded in the top border with a space on each side

Nested boxes or sub-sections use the lighter single-line set. Never use double-line box drawing (`║ ═`), it looks dated.

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

Apply this to: progress bars, sparkline colors, percentage text, container status indicators.

## Graphs and Sparklines

Use **braille characters** (U+2800–U+28FF) for graphs. Each braille character encodes a 2x4 dot grid, giving much higher resolution than block characters. This is what makes btop's graphs look smooth.

For sparklines (inline mini-graphs showing recent history), use a single row of braille characters. For larger graphs (CPU/memory over time), use multiple rows.

Fall back to block characters (`▁▂▃▄▅▆▇█`) only for simple bar indicators, not for time-series graphs.

**Progress bars** for current usage (disk, memory):

```
MEM  [████████████░░░░░░░░] 58.2%  3.8G / 6.5G
DISK [████████████████░░░░] 81.3%  38G / 47G      ← this one is yellow/red
```

Use `█` for filled, `░` for empty. Color the filled portion using the usage gradient.

## Layout Structure

The dashboard is the main view. It uses a grid of boxed panels:

```
╭─ Alerts (2) ───────────────────────────────────────────────────────────╮
│  ▲ CRIT  12:01  container_down   backup-cron exited (code 1)  ACTIVE  │
│  ▲ WARN  11:45  high_disk        disk usage at 81.3%          ACTIVE  │
╰───────────────────────────────────────────────────────────────────────╯
╭─ Containers ───────────────────────────────────╮╭─ Host ─────────────────────╮
│                                                ││ CPU  [████░░░░░░] 38.2%    │
│  myapp ──────────────────────── 4 running      ││ MEM  [██████░░░░] 58.2%    │
│  ● app-web        running    0.3%   128M       ││ DISK [████████░░] 81.3%    │
│  ● app-worker     running    1.2%    84M       ││ LOAD 0.82 1.24 0.95       │
│  ● postgres       running    0.8%   256M       ││ UP   14d 6h 32m           │
│  ● redis          running    0.1%    32M       ││                           │
│                                                ││ NET  ▼ 1.2MB/s ▲ 340KB/s  │
│  backups ────────────────────── 0 running      ││                           │
│  ○ backup-cron    exited     —       —         ││                           │
│                                                ││                           │
│  monitoring ─────────────────── 2 running      ││                           │
│  ● grafana        running    0.4%    92M       ││                           │
│  ● prometheus     running    0.6%   180M       ││                           │
│                                                ││                           │
╰────────────────────────────────────────────────╯╰───────────────────────────╯
╭─ Logs ─────────────────────────────────────────────────────────────────────╮
│  12:04:32 app-web       GET /api/users 200 12ms                           │
│  12:04:33 app-web       GET /api/users/5 200 8ms                          │
│  12:04:35 postgres      checkpoint complete: wrote 12 buffers             │
│  12:04:38 app-worker    processing job queue_id=4821                      │
│  12:04:41 app-web       POST /api/login 401 3ms                           │
╰───────────────────────────────────────────────────────────────────────────╯
```

### Alerts panel

Always at the top. This is the most important information on screen — if something is wrong, you should see it first. Shows only active (unresolved) alerts. If no active alerts, the panel collapses to a single line: `╭─ Alerts ── all clear ─╮`. Full alert history is on the dedicated Alerts view (tab 3).

### Container grouping

Containers are grouped by their Docker Compose project (read from the `com.docker.compose.project` label). Groups are collapsible with `Space`:

- Expanded shows a section header with a horizontal rule and group summary, followed by the containers
- Collapsed shows just the section header line
- Standalone containers (no compose label) appear under an "other" group at the bottom

### Tracking toggle (future — not in initial milestones)

Not all containers need monitoring. Press `t` on a selected container or group to toggle tracking on/off.

- **Tracked** (default): metrics collected, logs tailed, alert rules evaluated
- **Not tracked**: ignored by the agent entirely — no metrics, no logs, no alerts
- Untracked groups show `not tracked` in the section header
- This maps to the include/exclude config on the agent side. Toggling via TUI sends a `action:set_tracking` protocol message that updates the agent's runtime filter.
- **Do not implement until agent, protocol, and TUI are all functional (post-M4).**

### Layout priorities when resizing

1. Alerts panel is always visible at the top. It grows/shrinks based on active alert count.
2. Containers panel and Host panel share the middle row. Containers takes 60-70% width, Host gets the rest.
3. Logs panel takes full width below.
4. If terminal is too narrow (<100 cols), stack Host below Containers instead of beside it.
5. If terminal is too short, shrink Logs first, then collapse container groups.

## Container Status Indicators

```
●  green   — running, healthy
●  yellow  — running, unhealthy or restarting
○  red     — exited, dead, or error
○  gray    — paused, created, or removing
```

Use the filled circle `●` for active states, empty circle `○` for inactive.

## Text Formatting

- **Labels** are uppercase, muted or bold: `CPU`, `MEM`, `DISK`, `LOAD`, `NET`
- **Values** are default foreground, right-aligned within their column
- **Timestamps** in logs are muted/gray to not compete with the log message
- **Container names** in logs are colored with a consistent per-container color (hash the name to pick from a small palette of 6-8 distinct colors) so you can visually scan which container a log came from
- **Truncation**: long container names truncate with `…` on the right. Long log lines truncate with `…`. Never wrap inside a panel.

## Navigation

Consistent across all views:

```
Tab / 1-4        switch between views (dashboard, logs, alerts, detail)
j/k or ↑/↓       move selection up/down in lists
Enter             expand selected item (container detail, full log line, alert detail)
Space             collapse/expand a compose group
/                 start filtering/searching
q                 quit (with confirmation if connected to multiple servers)
?                 help overlay showing all keybindings
```

Show the active keybindings in a subtle footer bar at the bottom of the screen:

```
 1 Dashboard  2 Logs  3 Alerts  4 Detail │ Space Group  / Filter  ? Help  q Quit
```

This bar uses muted colors and doesn't compete with the main content.

## Log Viewer (Tab 2)

Full-screen view dedicated to log exploration. This is where you go when something is broken and you need to find out why.

```
╭─ Logs ── all containers ── 2,847 lines ──────────────────────────────────╮
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

## Container Detail View

When you select a container and press Enter, show a full-screen detail view:

```
╭─ app-web ── running ── up 14d 6h ── image: myapp:latest ──────────────╮
│                                                                        │
│  CPU ⣀⣀⣠⣤⣶⣿⣿⣶⣤⣠⣀⣀⣀⣀⣠⣤⣴⣶⣿⣿⣶⣤⣀⣀  2.3%                      │
│  MEM ⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿  128M / 512M limit         │
│  NET ▼ 42KB/s  ▲ 12KB/s                                               │
│  BLK ▼ 1.2MB/s ▲ 200KB/s                                              │
│  PID 1284  RESTARTS 0                                                  │
│                                                                        │
╰────────────────────────────────────────────────────────────────────────╯
╭─ Logs ─────────────────────────────────────────────────────────────────╮
│  (filtered to this container, same log viewer as main view)            │
╰────────────────────────────────────────────────────────────────────────╯
```

The braille graph for CPU/MEM shows the last N minutes of history using data from the agent.

## Responsive Design Rules

- **Minimum terminal size:** 80x24 (standard). Show a message if smaller.
- **Every `View()` function must use the current `tea.WindowSizeMsg` dimensions.** Never hardcode widths or heights.
- **Panel widths are proportional**, not fixed column counts. Calculate from available width.
- **Test at 80x24, 120x40, and 200x60** to catch layout issues at common terminal sizes.

## Things to Avoid

- Don't use emoji. Stick to Unicode symbols (●, ○, ▲, ▼, █, ░, braille).
- Don't use background colors on text blocks. It looks ugly on most terminals and clashes with user themes. Only use foreground colors.
- Don't animate or blink anything. Updates happen when new data arrives from the agent, not on a cosmetic timer.
- Don't add decorative borders or ASCII art. Every pixel is functional.
- Don't use more than 6-8 distinct colors total. Too many colors becomes noise.
- Never override the terminal's background color.
