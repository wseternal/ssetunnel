# Desktop Command Palette

## Goal
Users can press a meta key (Cmd on macOS, Ctrl on other platforms) in the Remote Desktop view to open a command palette overlay that intercepts all input, offering quick actions triggered by click or shortcut key — starting with "Refresh Screenshot" to force an immediate agent screen capture.

## Context
The Remote Desktop view currently captures screenshots on a 3-second deferred timer (input resets the timer). When a user needs an up-to-date screenshot immediately, they must wait for the defer delay. There is no mechanism for client-initiated capture or other session-level quick actions.

The input pipeline flows: Frontend → POST /remoteapp/connect-up → Server (wraps as FrameInput) → Agent (dispatches via robotgo). The agent's CaptureLoop uses an `inputReceived` channel to reset the defer timer.

## Success Criteria
- Pressing the meta key (Cmd/Ctrl) opens a command palette overlay centered on the remote desktop view
- While the palette is open, all keyboard and mouse input is intercepted (not forwarded to the agent)
- Each menu item can be triggered by mouse click or its shortcut key
- "Refresh Screenshot" (shortcut: `r`) forces the agent to capture and send a screenshot immediately, bypassing the defer timer
- "Toggle Fullscreen" (shortcut: `f`) toggles browser fullscreen for the desktop view
- "Disconnect" (shortcut: `q`) ends the remote desktop session
- Pressing Escape or clicking outside the palette closes it without triggering any action
- Pressing the meta key again toggles the palette closed
- The palette is visually consistent with the existing Mercury Console design system

## Constraints
- Must not break the existing deferred capture strategy for normal input flow
- The "refresh screenshot" control signal must not be forwarded to robotgo (it's a protocol-level control, not an input)
- Must work within the existing SSE + POST transport architecture
- Frontend changes are in the React SPA (Vite + TypeScript + MUI v9)

## Out of Scope
- Custom key bindings / user-configurable shortcuts
- Additional menu items beyond the initial three
- Agent-side capture quality/format changes
- Mobile/touch input support for the command palette

## Created
2026-09-04
