# Plan — Iteration 1

## Task 1: Backend Protocol Extension — `refresh_screenshot` control event

### 1a. Add `refresh_screenshot` to valid input types
**File:** `internal/remoteapp/input_validation.go`
**Change:** Add `"refresh_screenshot": true` to `validInputTypes` map.
**Acceptance:** `ValidateInputEventType("refresh_screenshot")` returns true.

### 1b. Add `forceCapture` channel to CaptureLoop
**File:** `internal/remoteapp/capture.go`
**Change:** Add `forceCapture <-chan struct{}` parameter to `CaptureLoop`. In the `for` loop's `select`, add a case:
```go
case <-forceCapture:
    writeLog("info", "force capture requested")
    transient, err := captureAndSend()
    if err != nil { return err }
    if transient {
        backoffDeadline = time.Now().Add(displayOffBackoff)
    } else {
        backoffDeadline = time.Time{}
    }
    drainTimer(deferTimer)
    if backoffDeadline.IsZero() {
        deferTimer.Reset(deferDelay)
    } else {
        deferTimer.Reset(displayOffBackoff)
    }
```
**Acceptance:** CaptureLoop accepts and handles immediate capture signal.

### 1c. Create forceCapture channel in ProxyRemoteApp and route control events
**File:** `internal/remoteapp/proxy.go`
**Change:**
- Create `forceCapture := make(chan struct{}, 1)` alongside `inputReceived`
- Pass `forceCapture` to `CaptureLoop(ctx, lw, inputReceived, forceCapture)`
- In the `FrameInput` case, before DispatchInput: if `event.Type == "refresh_screenshot"`, signal forceCapture and `continue` (skip DispatchInput and signalInput)
**Acceptance:** `refresh_screenshot` events trigger immediate capture without robotgo dispatch.

### 1d. Update capture_stub.go for signature compatibility
**File:** `internal/remoteapp/capture_stub.go`
**Change:** Update stub `CaptureLoop` to accept the new `forceCapture` parameter.
**Acceptance:** Stub compiles on all platforms.

### 1e. Tests
**File:** `internal/remoteapp/input_validation_test.go`, `internal/remoteapp/protocol_test.go`
**Change:** Add test for `ValidateInputEventType("refresh_screenshot")` returning true.
**Acceptance:** `go test ./internal/remoteapp/... -timeout 30s` passes.

---

## Task 2: Frontend Command Palette

### 2a. Add palette state and meta key detection
**File:** `frontend/console/src/App.tsx`
**Change:**
- Add state: `const [paletteOpen, setPaletteOpen] = useState(false)`
- Add ref: `const metaDownRef = useRef(false)`
- Modify the desktop keyboard `useEffect` handler to:
  - Detect meta key press (Cmd on Mac = `e.key === 'Meta'`, Ctrl on Win/Linux = `e.key === 'Control'`)
  - Track keydown/keyup via `metaDownRef` to toggle only on transition
  - When palette is open, intercept shortcut keys (r, f, q) and prevent forwarding
  - Handle Escape to close palette
- Modify `handleDesktopMouse` to early-return when `paletteOpen` is true
- Modify `handleDesktopKey` to early-return when `paletteOpen` is true

### 2b. Implement palette actions
**File:** `frontend/console/src/App.tsx`
**Change:**
- `handlePaletteAction(action: string)` function:
  - `'refresh-screenshot'`: `sendDesktopInput(sid, { type: 'refresh_screenshot' }, signal)` then close palette
  - `'toggle-fullscreen'`: call existing `toggleFullscreen()` then close palette
  - `'disconnect'`: call existing `disconnectDesktop()` then close palette

### 2c. Render palette overlay UI
**File:** `frontend/console/src/App.tsx`
**Change:**
- Render an overlay inside the desktop container when `paletteOpen && desktopConnected`:
  - Semi-transparent backdrop (click-to-close)
  - Centered card with 3 menu items
  - Each item: label + shortcut key chip (monospace font)
  - Hover state for mouse interaction
  - MUI components: Paper, List, ListItemButton, Typography, Chip
- Style consistent with existing console (light theme, MUI v9)

### 2d. Frontend build verification
**Acceptance:** `cd frontend/console && bun run build` succeeds without errors.

---

## Execution Order
1. Task 1a → 1b → 1c → 1d → 1e (backend first, TDD)
2. Task 2a → 2b → 2c → 2d (frontend, builds on backend protocol)
