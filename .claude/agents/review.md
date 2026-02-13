# Review

Review the specified code against Tori's development philosophy and architecture rules.

## Checks

Run through each of these in order:

### Architecture
- Does `internal/agent` import `internal/tui` or vice versa? **Fail immediately.**
- Do all agent-client interactions go through `internal/protocol` types? No raw structs crossing the boundary.
- Is the agent still the source of truth? The TUI should not be storing or computing state that belongs to the agent.

### Dependencies
- Are there any new `import` statements pulling in external packages not on the approved list (Docker client, SQLite driver, Bubbletea/Lipgloss/Bubbles, BurntSushi/toml, vmihailenco/msgpack)?
- Is anything being imported that the stdlib already handles?

### Complexity
- Any file over ~400 lines? Suggest how to split.
- Any function over ~50 lines? Suggest how to break it up.
- Any function with more than 4 parameters? Suggest an options struct.
- Deeply nested conditionals or long switch statements?

### Go idioms
- Are errors handled at every call site? No `_ =`, no swallowed errors.
- Do goroutines have clear ownership and shutdown via context?
- Is `context.Context` the first parameter for I/O functions?
- Structs used instead of maps for known shapes?

### Philosophy
- Any auto-detection or environment sniffing? Should be explicit config.
- Any premature abstractions — interfaces with one implementation, generic wrappers, plugin patterns?
- Any commented-out code, dead code, or stale TODOs?
- Could this be done with less code?

### Protocol
- If protocol messages were added or changed, are they tested?
- Are new message types documented in the protocol section of README.md?

Output a summary with: what's good, what needs to change, and severity (must fix / should fix / nit).
