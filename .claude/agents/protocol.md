# Protocol

Design a new protocol message or modify an existing one for agent-client communication.

## Process

1. **Read the Protocol section of README.md** for the current message catalog and conventions.
2. **Read `internal/protocol/`** to understand existing message types, encoding patterns, and how streaming vs request-response is structured.
3. **Determine the message pattern:**
   - **Streaming subscription** — agent continuously pushes data to client. Use for live/realtime data (metrics, logs, alerts).
   - **Request-response** — client asks, agent replies once. Use for historical queries, actions, and state lookups.
4. **Define the message** with:
   - Message type name (e.g., `query:logs`, `subscribe:metrics`, `action:set_tracking`)
   - Request fields (what the client sends)
   - Response fields (what the agent returns)
   - For streaming: what triggers a push, what the payload looks like per update
5. **Implement in `internal/protocol/`** with:
   - Go struct definitions for request and response
   - msgpack tags on all fields
   - Constructor or helper if the message has required fields
6. **Write tests** for serialization roundtrip — encode a message, decode it, verify equality.
7. **Update README.md** Protocol section with the new message type.

## Rules

- Keep messages flat. No nested structs more than one level deep.
- Use concrete types, not `interface{}` or `map[string]interface{}`.
- Field names are `snake_case` in msgpack, `PascalCase` in Go structs.
- Timestamps are always Unix milliseconds as `int64`.
- Every message includes a `type` field for routing.
