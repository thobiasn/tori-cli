# TUI

Design or modify a TUI view in `internal/tui/`.

## Principles

Rook's TUI is a view into the agent — it displays data, it doesn't own data. All state comes from the agent via protocol messages. The TUI should feel fast, information-dense, and require zero configuration.

## Process

1. **Read the existing views** in `internal/tui/` to understand the current structure, navigation pattern, and how views subscribe to agent data.
2. **Identify what data this view needs** from the agent. Map it to existing protocol messages. If no message exists, design it first using the `/protocol` agent.
3. **Design the layout** in terms of what information the user needs at a glance:
   - What's the most important thing on screen? Put it top-left or center.
   - What's secondary? Sidebar or bottom panel.
   - What needs real-time updates vs what is static until refreshed?
4. **Implement using Bubbletea's Elm architecture:**
   - `Model` — holds the view state. Only data needed for rendering, not agent state.
   - `Init()` — subscribe to the agent data streams this view needs.
   - `Update()` — handle key presses, mouse events, and incoming agent messages. Keep this thin — delegate to focused handler functions.
   - `View()` — pure render from model state. No side effects, no I/O.
5. **Style with Lipgloss.** Define a consistent color palette and reuse it:
   - Critical/error: red
   - Warning: yellow
   - Healthy/ok: green
   - Muted/secondary: gray
   - Don't overdo it. Most text should be the default terminal color.

## Layout Rules

- **Dashboard:** Container list with status indicators on the left, host metrics summary on the right. Sparklines or compact gauges for CPU/memory/disk/network if space allows.
- **Log viewer:** Full width. Container selector at the top, log stream below. Support filtering by typing. Newest at the bottom, auto-scroll unless the user has scrolled up.
- **Alert history:** Table with timestamp, severity, rule name, message, and status (active/acknowledged/resolved). Sort by most recent. Color-code severity.
- **Navigation:** Tab or number keys to switch between views. `q` or `Esc` to go back or quit. `?` for help. Keep it consistent across all views.

## Rules

- The TUI must never import `internal/agent`. All data comes through `internal/protocol`.
- Don't store historical data in the TUI. If the user switches away from the log view and comes back, re-request from the agent.
- Handle terminal resize gracefully. Every `View()` must respect the current terminal width and height.
- Truncate long container names and log lines rather than wrapping. Show full content on selection or hover.
- The TUI must remain responsive during slow agent responses. Use loading indicators for any request that might take more than 100ms.
- Test the `Update` function by sending specific `tea.Msg` values and asserting model state. Don't test `View()` output — it's too brittle.
