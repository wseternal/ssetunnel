# Remote App Control

## Goal
Users can view and interact with a remote agent's full desktop screen through the console web UI, seeing periodic screenshots (2-5 FPS) and sending full keyboard + mouse input (click, scroll, drag, key combos).

## Context
The existing "Cloud Shell" feature uses SSE-down + POST-up transport through the tunnel to spawn a PTY on the agent and proxy it to an xterm.js terminal in the browser. The same connect-session infrastructure (yamux streams, `connectSession` bridge, `handleConnect`/`handleConnectUp`) can be reused for screen capture + input replay.

**What exists:**
- Full tunnel transport: SSE-down, POST-up, yamux multiplexing, agent routing by `agent_id`
- `connectSession` bridge pattern with bidirectional data flow
- Console UI with agent selector, connect/disconnect lifecycle
- Auth middleware for all console API routes

**What's missing:**
- Agent-side screen capture via `robotgo` (screenshot → JPEG)
- Agent-side input replay via `robotgo` (mouse move, click, scroll, drag, keyboard)
- New target type (e.g., `__remote_app__`) to distinguish from shell/TCP targets
- Server-side binary frame streaming for screenshots (SSE base64 JPEG frames)
- Console UI canvas/image display instead of xterm.js terminal
- Input event forwarding (mouse + keyboard → POST)

## Success Criteria
- Console UI shows a "Remote Desktop" tab with agent selector and connect button
- When connected, the browser displays the agent's full screen as a live image updated at 2-5 FPS
- Left-click, right-click, and middle-click at the correct coordinates are replayed on the agent's desktop
- Keyboard input (arbitrary key combos like Ctrl+C, Alt+Tab, Cmd+V) is replayed on the agent's desktop
- Mouse scroll (up/down) and drag-and-drop work
- Disconnect button cleanly tears down the session
- All data flows through the existing tunnel (not direct HTTP to agent)

## Constraints
- Must use `github.com/go-vgo/robotgo` for screen capture and input
- Must reuse existing SSE-down + POST-up transport infrastructure
- Screenshots encoded as JPEG for bandwidth efficiency
- Agent must have display server access (X11/Wayland on Linux, WindowServer on macOS, Desktop on Windows)

## Out of Scope
- Multi-monitor selection (captures primary display only)
- Audio capture or playback
- Video recording/playback
- Clipboard synchronization
- File transfer
- Multi-user concurrent control

## Created
2026-07-31
