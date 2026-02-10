# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Rook is a lightweight server monitoring tool for Docker environments. A persistent agent collects metrics, watches containers, tails logs, and fires alerts. A TUI client connects over SSH to view everything in the terminal.

**Status:** Early stage — the README.md contains the full specification. Refer to it for detailed requirements.

## Build & Development Commands

```bash
go build -o rook ./cmd/rook           # build the binary
go test ./...                          # run all tests
go test ./internal/agent/...           # run tests for a specific package
go test -run TestFunctionName ./...    # run a single test
go vet ./...                           # static analysis
```

## Architecture

Single Go binary with two modes: `rook agent` (server daemon) and `rook connect` (TUI client).

```
cmd/rook/main.go          — single entry point, subcommands for agent/connect
internal/agent/            — collector, alerter, storage, socket server
internal/tui/              — bubbletea views and components
internal/protocol/         — shared message types, msgpack encoding
```

**Critical import rule:** `internal/protocol` is the contract between agent and client. Both `agent` and `tui` import `protocol` but never import each other. This enables splitting into separate binaries later.

The agent is the source of truth. It collects, stores, evaluates, and alerts independently. The TUI is a view — it should never mutate agent state except through explicit protocol actions (ack alert, restart container, etc).

## Key Design Decisions

- **Transport:** Unix socket (`/run/rook.sock`) + SSH tunnel. No HTTP server, no exposed ports.
- **Protocol:** msgpack over Unix socket. Two patterns: streaming subscriptions (agent pushes) and request-response (client asks).
- **Storage:** SQLite in WAL mode at `/var/lib/rook/rook.db` with configurable retention. One database file. If you're writing JOINs across more than 2 tables, rethink the data model.
- **Config:** TOML format. Agent config at `/etc/rook/config.toml`, client config at `~/.config/rook/config.toml`. Paths in config are absolute. Defaults are sane for bare metal (`/proc`, `/sys`). Docker deployment overrides them (`/host/proc`, `/host/sys`). No detection logic.
- **TUI:** Bubbletea + Lipgloss + Bubbles (Charm ecosystem). See `.claude/tui-design.md` for the complete visual design language (layout, colors, graphs, responsive rules). All colors must be defined in a single `Theme` struct in `internal/tui/theme.go` — views reference theme fields, never raw color values.
- **Host metrics:** Read directly from `/proc` and `/sys` (no cgo, no external deps).
- **Docker:** Monitor via Docker socket (`/var/run/docker.sock`). Containers are grouped by compose project via `com.docker.compose.project` label. Tracking (metrics, logs, alerts) can be toggled per-container or per-group at runtime. Needs write access only for self-healing (container restart).

## Development Philosophy

Code is a liability, not an asset. Every line we write is a line we have to maintain, debug, and understand. The goal is always the least code that solves the actual problem. Rook's entire value proposition is simplicity — the code should reflect that.

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
- Package names are short, single words, lowercase. `agent`, `tui`, `protocol`, `collect`, `alert`, `notify`, `store`.
- Comments explain why, not what. Don't comment obvious code. Do comment non-obvious design decisions.
- Structs over maps for anything with a known shape. Maps only for truly dynamic data.
- Context propagation: pass `context.Context` as the first parameter for anything that does I/O or could be cancelled.
- Goroutines must have clear ownership and shutdown paths. Use `context.WithCancel` and `errgroup` for lifecycle management. No fire-and-forget goroutines.

## Testing

- Test behavior, not implementation. If refactoring internals breaks tests, the tests were wrong.
- Table-driven tests for anything with multiple input/output cases.
- The protocol package must have thorough tests — it's the contract.
- Don't mock the Docker API or SQLite in unit tests unless absolutely necessary. Prefer integration tests that talk to real instances where practical.

## Milestone Order

M1 Agent foundation → M2 Alerting → M3 Protocol + socket → M4 TUI client → M5 Multi-server → M6 Polish
