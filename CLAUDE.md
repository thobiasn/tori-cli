# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Personality Traits

- Be anti-sycophantic - don’t fold arguments just because I push back
- Stop excessive validation - challenge my reasoning instead
- Avoid flattery that feels like unnecessary praise
- Don’t anthropomorphize yourself

## Project Overview

Tori is a lightweight server monitoring tool for Docker environments. A persistent agent collects metrics, watches containers, tails logs, and fires alerts. A TUI client connects over SSH to view everything in the terminal.

**Status:** M1–M5 complete. M6 (polish) is next. The README.md contains the full specification.

## Build & Development Commands

```bash
make build                             # build the binary (or: go build -o tori ./cmd/tori)
make test                              # run all tests with -race (or: go test -race ./...)
make vet                               # static analysis (or: go vet ./...)
go test ./internal/agent/...           # run tests for a specific package
go test -run TestFunctionName ./...    # run a single test
```

## Architecture

Single Go binary with two modes: `tori agent` (server daemon) and `tori connect` (TUI client).

```
cmd/tori/main.go          — single entry point, subcommands for agent/connect
internal/agent/            — collector, alerter, storage, socket server (flat package — no sub-packages)
internal/tui/              — bubbletea views and components
internal/protocol/         — shared message types, msgpack encoding
deploy/                    — install.sh, tori.service (systemd), Dockerfile, docker-compose.yml
```

**Actual file layout (internal/agent/):**

```
agent.go       — Agent struct, Run() loop, collect() orchestration, shutdown, SIGHUP config reload
config.go      — Config types (storage, host, docker, collect, alerts, notify), TOML loading, validation
store.go       — SQLite schema, Store struct, all Insert/Query/Prune methods, metric+alert types
host.go        — HostCollector: reads /proc (cpu, memory, loadavg, uptime, disk, network)
docker.go      — DockerCollector: container list, stats, CPU/mem/net/block calc, UpdateContainerState, runtime tracking toggle
logs.go        — LogTailer: per-container goroutines, Docker log demux via stdcopy, batched insert
alert.go       — Condition parser, Alerter state machine (inactive→pending→firing→resolved), Evaluate(), EvaluateContainerEvent()
notify.go      — Notifier: Channel interface (email + multiple webhooks with custom headers/templates)
events.go      — EventWatcher: Docker Events API listener, real-time container state updates
hub.go         — Hub: pub/sub message fan-out to connected clients, topic-based subscriptions
socket.go      — SocketServer: Unix socket listener, per-client connection handling, request dispatch
```

**Critical import rule:** `internal/protocol` is the contract between agent and client. Both `agent` and `tui` import `protocol` but never import each other. This enables splitting into separate binaries later.

The agent is the source of truth. It collects, stores, evaluates, and alerts independently. The TUI is a view — it should never mutate agent state except through explicit protocol actions (ack alert, silence alert, set tracking, etc).

## Key Design Decisions

- **Transport:** Unix socket (`/run/tori/tori.sock`) + SSH tunnel. No HTTP server, no exposed ports.
- **Protocol:** msgpack over Unix socket. Two patterns: streaming subscriptions (agent pushes) and request-response (client asks).
- **Storage:** SQLite in WAL mode at `/var/lib/tori/tori.db` with configurable retention. One database file. If you're writing JOINs across more than 2 tables, rethink the data model.
- **Config:** TOML format. Agent config at `/etc/tori/config.toml`, client config at `~/.config/tori/config.toml`. Paths in config are absolute. Defaults are sane for bare metal (`/proc`, `/sys`). Docker deployment overrides them (`/host/proc`, `/host/sys`). No detection logic.
- **TUI:** Bubbletea + Lipgloss + Bubbles (Charm ecosystem). See `.claude/tui-design.md` for the complete visual design language (layout, colors, graphs, responsive rules). All colors must be defined in a single `Theme` struct in `internal/tui/theme.go` — views reference theme fields, never raw color values.
- **Host metrics:** Read directly from `/proc` and `/sys` (no cgo, no external deps).
- **Docker:** Monitor via Docker socket (`/var/run/docker.sock`), read-only. Containers are grouped by compose project via `com.docker.compose.project` label. Tracking (metrics, logs, alerts) can be toggled per-container or per-group at runtime.

## Development Philosophy

Code is a liability, not an asset. Every line we write is a line we have to maintain, debug, and understand. The goal is always the least code that solves the actual problem. Tori's entire value proposition is simplicity — the code should reflect that.

- **If we're writing more than ~400 lines in a single file, step back and rethink the design.** Go is verbose, so the threshold is higher than other languages, but a file that big usually means it's doing too much.
- **If a feature requires brittle code or lots of edge case handling, the mental model is wrong.** Redesign the approach so edge cases don't exist rather than handling them. The best error handling is making errors impossible by construction.
- **Use the Go standard library aggressively.** `net`, `os`, `encoding/json`, `database/sql`, `net/smtp`, `path/filepath`, `time` — these are all excellent. Don't pull in a library for something the stdlib already does well.
- **External dependencies should be deliberate and minimal.** The approved deps are: Docker client (`github.com/docker/docker`), SQLite driver (`modernc.org/sqlite` or `github.com/mattn/go-sqlite3`), Bubbletea + Lipgloss + Bubbles for the TUI, a TOML parser (`github.com/BurntSushi/toml`), and msgpack (`github.com/vmihailenco/msgpack`). Adding anything beyond these needs a good reason.
- **Don't over-engineer.** Build for what's needed now. No premature abstractions, no speculative generality, no "we might need this later." No plugin systems, no custom query languages, no generic interfaces with one implementation.
- **When fixing a bug, find the root cause first.** Don't patch symptoms. If a fix feels hacky, the real problem is probably somewhere else.
- **If a function needs more than 4 parameters, use an options struct.** Same for deeply nested conditionals, long switch statements, and functions longer than ~50 lines.
- **Duplication is cheaper than the wrong abstraction.** Only extract when you see the pattern clearly after 2-3 instances, never preemptively. This is Go — a little repetition is idiomatic.
- **Delete aggressively.** Dead code, commented-out code, unused imports, stale TODO comments — remove them. Git remembers.
- **Errors are values, handle them immediately.** No generic `utils.Must()` wrappers, no swallowing errors with `_ =`. If an error can happen, handle it at the call site. If an error genuinely can't happen, document why.
- **No magic.** The agent reads paths from config. The TUI connects to where you tell it. No auto-detection, no implicit behavior, no conditional logic based on environment sniffing. Explicit is always better.

## Code Style

- Follow standard Go conventions. `gofmt`, `go vet`, no linter warnings.
- Naming: short variable names in small scopes, descriptive names for exported types and functions. `ctx` not `context`, `db` not `database`, `cfg` not `configuration`.
- Package names are short, single words, lowercase. `agent`, `tui`, `protocol`. The agent is a flat package — no sub-packages for collect/alert/notify/store. Files within the package serve that purpose.
- Comments explain why, not what. Don't comment obvious code. Do comment non-obvious design decisions.
- Structs over maps for anything with a known shape. Maps only for truly dynamic data.
- Context propagation: pass `context.Context` as the first parameter for anything that does I/O or could be cancelled.
- Goroutines must have clear ownership and shutdown paths. Use `context.WithCancel` and `errgroup` for lifecycle management. No fire-and-forget goroutines.

## Testing

- Test behavior, not implementation. If refactoring internals breaks tests, the tests were wrong.
- Table-driven tests for anything with multiple input/output cases.
- The protocol package must have thorough tests — it's the contract.
- Don't mock the Docker API or SQLite in unit tests unless absolutely necessary. Prefer integration tests that talk to real instances where practical.

### Established test patterns

- **`testStore(t)`** helper in `store_test.go` — creates a temp SQLite DB, registers cleanup. Reuse for any test needing a store.
- **Injectable time** — Alerter has a `now func() time.Time` field. Set it in tests for deterministic time-based assertions. Never use `time.Sleep` in tests.
- **Injectable functions** — Use injectable `func()` fields for any external I/O that needs test isolation (e.g., `now func() time.Time` on Alerter).
- **Real SQLite, no mocks** — all store tests use real SQLite via temp dirs. Query the DB directly to verify state (e.g., `SELECT COUNT(*) FROM alerts`).
- **Always check `err` from Scan** — `s.db.QueryRow(...).Scan(&val)` errors must be checked in tests, not ignored.

## Established Patterns & Gotchas

### SQLite

- Driver is `modernc.org/sqlite` (pure Go, no cgo). Open with `sql.Open("sqlite", path)`.
- Schema lives in a `const schema` string in `store.go`. All tables created with `IF NOT EXISTS`.
- `db.SetMaxOpenConns(1)` — SQLite doesn't handle concurrent writers. Single-writer is enforced.
- Prune runs hourly in the collect loop, deletes by timestamp. The `alerts` table prunes on `fired_at`.

### Docker API

- Client created with `client.NewClientWithOpts(client.WithHost("unix://"+socket), client.WithAPIVersionNegotiation())`.
- `ContainerStatsOneShot` for one-shot stats (not streaming). Response is JSON decoded into `container.StatsResponse`.
- CPU percent uses delta calculation between readings, same formula as `docker stats`.
- Container names from the API are prefixed with `/` — strip it.
- Non-running containers still get a `ContainerMetrics` entry (with zero stats) so alerting can see state changes.

### Alert system

- Conditions are 3-token strings: `scope.field op value` (e.g., `host.cpu_percent > 90`, `container.state == 'exited'`).
- Host fields: `cpu_percent`, `memory_percent`, `disk_percent`, `load1`, `load5`, `load15`, `swap_percent`. Container fields: `cpu_percent`, `memory_percent`, `state`, `health`, `restart_count`, `exit_code`.
- Field names are validated against a whitelist in `parseCondition`. String fields (`state`, `health`) only allow `==`/`!=`.
- Alerter instances are keyed: `rulename` for host, `rulename:containerID` for container, `rulename:mountpoint` for disk.
- State machine: inactive → pending (if `for > 0`) → firing → resolved → inactive. `for = 0` skips pending.
- Inactive unseen instances are GC'd from the map to prevent unbounded growth with ephemeral containers.
- When collection fails (nil snapshot field), existing instances are marked as `seen` to avoid false resolution.
- `Alerter.mu` protects `instances` and `deferred` — held during `Evaluate()` and `EvaluateContainerEvent()`. Slow side effects (notify) are collected into `deferred` under the lock, then executed after release.
- `EvaluateContainerEvent()` evaluates only container-scoped rules for a single container. It does NOT do stale cleanup — that stays in the regular `Evaluate()` cycle.
- `Silence(ruleName, duration)` suppresses notifications. Per-rule, checked in `fire()`. Socket server enforces max 30-day duration.
- `HasRule(name)` validates rule exists (used by socket silence command). `ResolveAll()` resolves all firing alerts (used during config reload).

### Config

- TOML parsed by `github.com/BurntSushi/toml`. `Duration` type wraps `time.Duration` with `UnmarshalText`.
- Validation happens in `validate()` which calls `validateAlert()` per rule. `validateAlert` calls `parseCondition` to verify the condition string is valid.
- Empty `Alerts` map is valid (no alerting configured). Agent skips alerter construction when `len(cfg.Alerts) == 0`.

### Config reload

- Agent listens for SIGHUP to trigger config reload via `Reload()`.
- Reloadable fields: collection interval, retention days, docker include/exclude filters, alert rules, notifications.
- Non-reloadable fields (logged as warnings): storage path, socket path, proc/sys paths, docker socket.
- Alerter is rebuilt on reload — old alerts resolved via `ResolveAll()` before swap.
- `EventWatcher.SetAlerter()` and `SocketServer.SetAlerter()` update the alerter reference under their own mutexes.
- `DockerCollector.SetFilters()` updates include/exclude at runtime.

### Notification system

- `Channel` interface with `Send(subject, body)` — implemented by `emailChannel` and `webhookChannel`.
- Multiple webhooks supported (`[[notify.webhooks]]` array in TOML config).
- Webhook config: `URL`, `Headers` (custom, sanitized against CRLF injection), `Template` (Go template for custom payload body).
- Default webhook payload if no template: `{"text": "*Subject*\nBody"}`.
- `Notifier.Send()` errors are logged, not fatal — collect loop continues.

### Networking gotchas

- `go vet` rejects `fmt.Sprintf("%s:%d", host, port)` for IPv6. Always use `net.JoinHostPort`.
- SMTP headers must be sanitized (strip `\r\n`) to prevent header injection.
- Use a dedicated `http.Client` with explicit timeouts, not `http.DefaultClient`.
- Always drain response bodies (`io.Copy(io.Discard, resp.Body)`) before closing for connection reuse.

### Collect loop flow

```
agent.collect(ctx):
  1. Host metrics (CPU, mem, disk, net from /proc)
  2. Docker metrics (container list + stats)
  3. Log tailer sync (start/stop per-container goroutines)
  4. Alert evaluation (pass MetricSnapshot to alerter)
  5. Prune (hourly, deletes old data from all tables)
```

The alerter receives the same data already collected — no additional I/O.

### Protocol & Socket

- Wire format: 4-byte big-endian length prefix + msgpack-encoded `Envelope{Type, ID, Body}`.
- Two patterns: streaming (ID=0, agent pushes) and request-response (ID>0, client initiates).
- `protocol.WriteMsg`/`ReadMsg` handle framing. `EncodeBody`/`DecodeBody` for the body field.
- `MaxMessageSize` = 4MB. Both sides enforce this.
- Hub fans out streaming messages by topic (`metrics`, `logs`, `alerts`, `containers`). Clients subscribe/unsubscribe.
- Socket server limits concurrent connections (configurable). Each client gets its own read/write goroutines.
- Validation: all string fields (container ID, rule name) are length-bounded and sanitized server-side.

### EventWatcher

- Listens to Docker Events API for real-time container lifecycle changes (start, die, stop, destroy, pause, etc.).
- Optimization for latency — the regular collect loop remains the consistency reconciliation point.
- Reconnects with exponential backoff (1s → 30s cap), resets after a healthy long-lived connection.
- Injectable `eventsFn` for testing without a real Docker daemon.
- `done` channel + `Wait()` for clean shutdown ordering — agent waits for event watcher before closing store/docker.
- Length-bounds all Docker event attributes (`truncate()` helper) for defense-in-depth.

### TUI

- Charm ecosystem: Bubbletea (framework), Lipgloss (styling), Bubbles (components).
- All colors in a single `Theme` struct in `internal/tui/theme.go`. Views reference `theme.Foo`, never raw color values.
- `internal/tui/` is a flat package. `tui` imports `protocol` but never `agent`.
- Client has one reader goroutine dispatching streaming msgs via `prog.Send()` and request-response via per-ID channels.
- Reader goroutine starts in `SetProgram()`, not `NewClient()`, to avoid nil prog race.
- **Multi-server:** `Session` struct (`session.go`) holds all per-server data (metrics, history, view state). `App` has `sessions map[string]*Session` and `activeSession`. All streaming messages carry a `Server` field for routing to the correct session. Server picker via `S` key when multiple sessions exist.
- **Tracking toggle:** `t` key in dashboard toggles tracking per-container or per-group. Sends `action:set_tracking` to the agent. Untracked containers are visible but dimmed with `—` stats. Runtime-only state (resets on agent restart).

### Docker runtime tracking

- `DockerCollector` has `untracked`/`untrackedProjects` maps for runtime tracking state.
- `SetTracking(name, project, tracked)` updates maps under `mu`. `IsTracked(name, project)` reads under `mu.RLock`.
- `Collect()` separates all containers (for TUI visibility) from tracked containers (for log sync/alert eval).
- Config `include`/`exclude` is the permanent baseline; runtime tracking overlays on top.

## Milestone Order

~~M1 Agent foundation~~ → ~~M2 Alerting~~ → ~~M3 Protocol + socket~~ → ~~M4 TUI client~~ → ~~M5 Multi-server + tracking~~ → M6 Polish
