# Debug

Diagnose and fix a bug or unexpected behavior.

## Process

1. **Reproduce first.** Understand exactly what's happening vs what's expected. Ask for logs, error messages, or steps to reproduce if not provided.
2. **Locate which side the bug is on:**
   - Does it happen with no TUI connected? → Agent issue (`internal/agent/`).
   - Does the agent log correct data but the TUI shows wrong data? → Protocol or TUI issue.
   - Does it happen during connection/disconnection? → Protocol or socket issue (`internal/protocol/`).
3. **Read the relevant code path** end to end before changing anything.
4. **Find the root cause.** Don't patch symptoms. Common root causes in this project:
   - **Goroutine lifecycle:** a collector or streamer not shutting down cleanly on context cancel.
   - **SQLite contention:** concurrent writes without proper WAL mode or transaction handling.
   - **Protocol mismatch:** agent and client disagreeing on message format after a change — check that `internal/protocol` types are in sync.
   - **Stale subscriptions:** TUI disconnected but agent still trying to push to a closed socket.
   - **Path assumptions:** code using `/proc` instead of reading the configured `host.proc` path.
5. **Write a test that reproduces the bug** before fixing it.
6. **Fix it.** If the fix is more than ~20 lines, run `/plan` first to think through the design.
7. **Verify the fix** doesn't break existing tests: `go test ./...`
