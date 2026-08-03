# Iteration 1 Plan — Remote App Control

## Architecture Overview

```
Browser (Remote Desktop tab)
  │  SSE-down: base64 JPEG frames
  │  POST-up: JSON input events
  ▼
┌─────────────────────────────────────────────────────────┐
│  Server (remoteapp handlers)                             │
│  • GET /console/api/v1/remoteapp/connect → SSE stream    │
│  • POST /console/api/v1/remoteapp/connect-up → input     │
│  • Frame-aware bridge: yamux typed frames ↔ HTTP         │
└──────────┬──────────────────────────────────────────────┘
           │  yamux stream (typed length-prefixed frames)
           ▼
┌─────────────────────────────────┐
│  Agent (proxyRemoteApp)          │
│  • Goroutine 1: capture → JPEG  │
│  • Goroutine 2: read input →    │
│    dispatch via robotgo          │
└─────────────────────────────────┘
```

## Wire Protocol (yamux stream)

The agent-server yamux stream uses typed length-prefixed frames:

| Direction | Frame Type | Format |
|-----------|-----------|--------|
| Agent → Server | Screenshot | `[0x01][4-byte BE uint32 length][JPEG bytes]` |
| Server → Agent | Input Event | `[0x02][4-byte BE uint32 length][JSON bytes]` |

This avoids delimiter collisions (JPEG binary contains `\n` and `\x00`).

## Input Event JSON Schema

```json
{
  "type": "mouse_move" | "mouse_click" | "mouse_scroll" | "mouse_drag" | "key_tap" | "key_toggle" | "type_text",
  "x": 500, "y": 300,           // mouse coordinates (agent screen pixels)
  "button": "left" | "right" | "middle" | "wheelUp" | "wheelDown",
  "direction": "up" | "down" | "left" | "right", // scroll direction
  "amount": 3,                   // scroll amount
  "key": "enter" | "tab" | ...,  // key name (robotgo key names)
  "modifiers": ["ctrl", "shift"], // modifier keys
  "text": "hello",               // for type_text
  "state": "down" | "up"         // for key_toggle, mouse drag
}
```

## Build Tag Strategy

Use `remoteapp` build tag to gate robotgo dependency:
- `//go:build remoteapp` — real implementation with robotgo
- `//go:build !remoteapp` — stub returning `ErrNotSupported`
- Default `go build ./...` succeeds without robotgo (CI-safe)
- `go build -tags remoteapp ./...` enables the feature

## Task Breakdown

### Task 1: Wire Protocol + Types
**Files:** `internal/remoteapp/protocol.go`
- Define frame type constants (`FrameScreenshot = 0x01`, `FrameInput = 0x02`)
- `WriteFrame(w io.Writer, frameType byte, data []byte) error` — writes typed length-prefixed frame
- `ReadFrame(r io.Reader) (frameType byte, data []byte, err error)` — reads typed length-prefixed frame
- Max frame size constant: 4 MiB (safety limit for JPEG frames)
- `InputEvent` struct with JSON tags matching the schema above
- Unit test: round-trip WriteFrame → ReadFrame

### Task 2: Screen Capture Module
**Files:** `internal/remoteapp/capture.go` (`//go:build remoteapp`), `internal/remoteapp/capture_stub.go` (`//go:build !remoteapp`)
- `CaptureLoop(ctx context.Context, stream io.Writer, fps int) error` — captures screenshots at fps rate, writes typed frames
- Uses `robotgo.CaptureImg()` → `robotgo.SaveJpeg(img, quality)` or direct JPEG encoding via `image/jpeg`
- JPEG quality: 50 (balance bandwidth vs clarity at 2-5 FPS)
- `GetScreenSize() (width, height int)` — returns agent screen dimensions (for coordinate scaling)
- Stub version: `CaptureLoop` returns `ErrNotSupported`, `GetScreenSize` returns `(0, 0)`
- `var ErrNotSupported = errors.New("remote app not supported: build with -tags remoteapp")`

### Task 3: Input Replay Module
**Files:** `internal/remoteapp/input.go` (`//go:build remoteapp`), `internal/remoteapp/input_stub.go` (`//go:build !remoteapp`)
- `DispatchInput(event InputEvent, screenWidth, screenHeight int) error` — dispatches input event via robotgo
- Mouse: `robotgo.Move(x, y)`, `robotgo.Click(button)`, `robotgo.ScrollDir(amount, direction)`, `robotgo.Toggle(button, state)`
- Keyboard: `robotgo.KeyTap(key, modifiers...)`, `robotgo.KeyToggle(key, state)`, `robotgo.Type(text)`
- Coordinate validation: clamp x/y to `[0, screenWidth)` / `[0, screenHeight)`
- Key name validation: whitelist of known robotgo key names to prevent injection
- Stub version: `DispatchInput` returns `ErrNotSupported`

### Task 4: Agent Proxy
**Files:** `internal/remoteapp/proxy.go`, `internal/agent/shell.go` (add constant), `internal/agent/agent.go` (add routing)
- `ProxyRemoteApp(stream net.Conn)` in `internal/remoteapp/proxy.go`:
  - Get screen dimensions via `GetScreenSize()`
  - Write screen dimensions as first frame (type 0x03, JSON `{width, height}`)
  - Start `CaptureLoop` goroutine (writes screenshot frames to stream)
  - Main goroutine: `ReadFrame` loop → dispatch input events via `DispatchInput`
  - When stream closes or ctx cancels: cancel capture loop, close stream
- Add `TargetRemoteApp = "__remote_app__"` to `internal/agent/shell.go`
- Add routing in `internal/agent/agent.go` proxy method:
  ```go
  if target == TargetRemoteApp {
      remoteapp.ProxyRemoteApp(stream)
      return
  }
  ```
- Build constraint: when `!remoteapp` tag, `ProxyRemoteApp` logs error and closes stream

### Task 5: Server Handlers
**Files:** `internal/server/remoteapp.go`, `internal/consoleserver/consoleserver.go`
- `RemoteAppConnectHandler() http.Handler` — wraps `handleRemoteApp` with forced target (same pattern as `ShellConnectHandler`)
- `RemoteAppConnectUpHandler() http.Handler` — accepts JSON input events via POST, writes typed frames to connect session's up pipe
- `handleRemoteApp(w, r)` — custom handler similar to `handleConnect` but:
  - Bridge goroutine reads from `cs.up` pipe, wraps POST body as typed input frame, writes to yamux
  - SSE loop reads typed frames from yamux stream:
    - Screenshot frame (0x01) → base64 encode → SSE `data:` frame
    - Screen info frame (0x03) → base64 encode → SSE `data:` frame with `event: screeninfo`
  - No resize channel needed
- Route registration in `consoleserver.go`:
  ```go
  r.Handle("/console/api/v1/remoteapp/connect", userAuth(th.RemoteAppConnectHandler())).Methods("GET")
  r.Handle("/console/api/v1/remoteapp/connect-up", userAuth(th.RemoteAppConnectUpHandler())).Methods("POST")
  ```

### Task 6: Frontend Remote Desktop Tab
**Files:** `frontend/console/src/App.tsx`, rebuild dist
- New state variables: `desktopAgent`, `desktopConnected`, `desktopSessionId`, `screenWidth`, `screenHeight`
- New refs: `desktopAbortRef`, `desktopImgRef` (HTMLImageElement or canvas)
- Connect function: similar to `connectShell` but:
  - SSE URL: `/console/api/v1/remoteapp/connect?id=...&agent=...`
  - On SSE frame: decode base64 → set as `<img>` src via blob URL or data URI
  - Handle `event: screeninfo` to get screen dimensions
- Mouse event handlers on `<img>` element:
  - `onClick` → compute agent coordinates → POST `{type: "mouse_click", x, y, button}`
  - `onContextMenu` → right-click
  - `onWheel` → scroll
  - `onMouseDown`/`onMouseMove`/`onMouseUp` → drag
- Keyboard: attach `keydown`/`keyup` listeners when connected → POST `{type: "key_tap", key, modifiers}`
- Coordinate scaling: `agentX = (clickX / imgWidth) * screenWidth`, `agentY = (clickY / imgHeight) * screenHeight`
- Disconnect: abort controller cleanup
- Tab position: after Shell tab (admin tabIndex 5, user tabIndex 4)
- Desktop icon: `<DesktopWindowsIcon />` from MUI
- Rebuild: `cd frontend/console && bun run build`

### Task 7: Tests and Build Verification
- `internal/remoteapp/protocol_test.go`: round-trip WriteFrame/ReadFrame
- `internal/remoteapp/input_test.go`: JSON parsing, coordinate clamping, key whitelist (build-tagged)
- `go build ./...` succeeds (no remoteapp tag)
- `go build -tags remotego ./...` succeeds (with robotgo)
- `go test ./internal/remoteapp/... -v` passes
- `go vet ./...` passes
- `cd frontend/console && bun run build` succeeds

## Rejected Alternatives

1. **Reuse handleConnect directly for remote app**: Rejected because the bridge goroutine needs frame-aware forwarding (typed frames, not raw byte copying). The shell bridge copies raw bytes; remote app needs to parse agent frames and wrap input as typed frames.

2. **WebSocket for screenshot streaming**: Rejected because it bypasses the existing tunnel infrastructure. The goal requires all data to flow through the SSE-down + POST-up transport.

3. **No build tag (always depend on robotgo)**: Rejected because robotgo requires CGo or platform-specific purego backends, making `go build ./...` fail on CI without extra setup. Build tag keeps CI clean.

4. **Delimiter-based wire protocol (\n or \x00)**: Rejected because JPEG binary data can contain any byte value. Length-prefixed is the only safe option.

## Risk Assessment

1. **robotgo platform support**: robotgo requires display server access. Headless CI cannot test the real capture path. Mitigation: build tag + stub for CI; real testing requires a display.
2. **Bandwidth**: At 5 FPS with quality 50, each JPEG is ~50-150KB for a 1920x1080 screen. That's 250-750 KB/s downstream. The tunnel's 1 MiB batch ceiling handles this.
3. **Input latency**: POST round-trip adds ~50-200ms latency. Acceptable for monitoring/clicking, not for real-time interaction.
4. **Frontend image flicker**: Rapidly replacing `<img>` src may cause flicker. Mitigation: use double-buffering with two `<img>` elements or canvas `drawImage`.
