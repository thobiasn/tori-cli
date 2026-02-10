# Security

Audit code for security concerns specific to Rook's threat model.

## Threat Model

Rook runs with elevated access: Docker socket (effectively root), host `/proc` and `/sys`, and stores potentially sensitive log data. The main security guarantee is that Rook exposes zero network ports — all access is gated by SSH. The goal is to not weaken that posture.

## Checks

### Docker Socket
- Is the Docker socket ever exposed beyond the agent process? It should only be used by the collector and self-healing action handler.
- Are self-healing actions (container restart, command execution) gated by explicit config? The agent must never restart or modify containers unless the user configured it.
- If running in Docker, is the socket mounted `:ro` when self-healing is disabled?
- Could a crafted TUI request trigger unintended Docker operations? Validate that only the explicitly defined protocol actions (`action:restart_container`) can write to Docker, and that they check against the alert config.

### Unix Socket
- What are the file permissions on `/run/rook.sock`? Should be `0660` or stricter.
- Is there any way to interact with the agent other than the Unix socket? There should not be.
- Are client connections authenticated or authorized in any way? Currently SSH is the auth layer — document this assumption explicitly if relying on it.

### Config and Credentials
- Does the agent config contain secrets (SMTP passwords, webhook URLs)? Verify file permissions guidance is in the README.
- Are credentials ever logged, included in error messages, or written to SQLite?
- Are credentials held only in the notifier package, not passed through protocol messages?

### SQLite and Log Data
- Could log contents contain tokens, API keys, PII, or other sensitive application data? They can and will.
- Is the database file permission restrictive? Should be `0600`.
- Does the retention policy actually delete old data, or just mark it? Verify hard deletes with `VACUUM` or `DELETE`.
- Is there any way for a TUI client to extract more log data than the retention policy should allow?

### Protocol
- Can a malformed msgpack message crash the agent? All deserialization must handle errors gracefully.
- Is there any message type that could cause unbounded resource usage (memory, disk, CPU) on the agent? For example, a `query:logs` with no time bounds fetching the entire database.
- Are all protocol actions idempotent or safe to retry?

### General
- No hardcoded credentials, tokens, or secrets anywhere in the codebase.
- No `os/exec` calls with user-supplied input without sanitization.
- No `unsafe` package usage.
- Goroutines handling client connections must not leak on disconnect.

Output a security assessment with: findings, severity (critical / high / medium / low), and recommended fix.
