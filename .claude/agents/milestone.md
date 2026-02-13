# Milestone

Assess current project state and determine what to build next.

## Process

1. **Read README.md** for the milestone plan and full feature spec.
2. **Scan the codebase** to understand what exists:
   - `ls -R cmd/ internal/` to see the file structure
   - Check for TODO/FIXME comments: `grep -r "TODO\|FIXME" internal/`
   - Check test coverage: `go test -cover ./...`
   - Check if it builds: `go build ./cmd/tori`
   - Check if tests pass: `go test ./...`
3. **Map what exists to the milestones:**

   **M1 — Agent foundation:**
   - [ ] Host metric collection (CPU, memory, disk, network from `/proc` and `/sys`)
   - [ ] Docker container discovery and stats via Docker socket
   - [ ] SQLite storage with WAL mode
   - [ ] TOML config loading
   - [ ] systemd service file

   **M2 — Alerting:**
   - [ ] Alert rule evaluation from config
   - [ ] Email/SMTP notifications
   - [ ] ~~Self-healing actions~~ (removed — Docker socket is read-only)
   - [ ] Alert persistence in SQLite

   **M3 — Protocol + socket:**
   - [ ] Unix socket server in agent
   - [ ] msgpack message encoding/decoding
   - [ ] Streaming subscription handlers
   - [ ] Request-response handlers

   **M4 — TUI client:**
   - [ ] SSH tunnel management
   - [ ] Dashboard view (containers + host metrics)
   - [ ] Log viewer with filtering
   - [ ] Alert history view

   **M5 — Multi-server:**
   - [ ] Client-side server config
   - [ ] Server switcher in TUI
   - [ ] Concurrent connections

   **M6 — Polish:**
   - [ ] Webhook/Slack notifications
   - [ ] Alert silencing/acknowledgement
   - [ ] Config reload without restart
   - [ ] Install script

4. **Output:** A status report with what's done, what's partially done, what's next, and a recommended task to work on. The recommended task should be the smallest useful increment within the current milestone.
