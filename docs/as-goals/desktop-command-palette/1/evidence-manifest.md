# Evidence Manifest — Iteration 1

## Gate Status

| Gate | Status | Evidence | Owner |
|------|--------|----------|-------|
| Gate 1: Command Palette UI | ✅ Pass | `frontend/console/src/App.tsx` (lines ~1853-1924: palette overlay with 3 menu items), frontend build green | Engineer + Frontend Engineer |
| Gate 2: Input Interception | ✅ Pass | `frontend/console/src/App.tsx` (meta key handler, early-return in handleDesktopMouse/handleDesktopKey, Escape/click-outside dismiss) | Engineer + Frontend Engineer |
| Gate 3: Refresh Screenshot Protocol | ✅ Pass | `internal/remoteapp/input_validation.go` (whitelist), `internal/remoteapp/capture.go` (forceCapture channel), `internal/remoteapp/proxy.go` (routing), tests in `input_validation_test.go` and `protocol_test.go` | Engineer |

## Gate Evidence Detail

### Gate 1: Command Palette UI
- **Palette component:** `frontend/console/src/App.tsx` renders overlay with `paletteOpen && desktopConnected` condition
- **3 menu items:** "Refresh Screenshot" (R), "Toggle Fullscreen" (F), "Disconnect" (Q) — all present with labels and shortcut chips
- **Frontend build:** `bun run build` succeeds — `dist/index.html` 1,278.17 kB
- **MUI styling:** Uses Paper, Box, Typography, Chip components consistent with existing console theme

### Gate 2: Input Interception
- **Meta key toggle:** `e.key === 'Meta' || (e.key === 'Control' && !isMac)` in keydown handler, with `metaDownRef` to prevent rapid toggling
- **Input suppression:** `handleDesktopMouse` and `handleDesktopKey` both early-return when `paletteOpen` is true
- **Shortcut keys:** `r`, `f`, `q` mapped to `handlePaletteAction` when palette is open
- **Escape dismiss:** `e.key === 'Escape'` calls `setPaletteOpen(false)`
- **Click-outside dismiss:** Backdrop `onClick={() => setPaletteOpen(false)}` with Paper `onClick={(e) => e.stopPropagation()}`
- **Disconnect cleanup:** `resetDesktopState` now resets `paletteOpen` and `metaDownRef` (fixed in `c095be9e`)

### Gate 3: Refresh Screenshot Protocol
- **Validation:** `refresh_screenshot` added to `validInputTypes` in `input_validation.go`
- **Dispatch skip:** `ProxyRemoteApp` checks `event.Type == "refresh_screenshot"` before `DispatchInput`, routes to `signalForceCapture()` instead
- **CaptureLoop:** New `forceCapture <-chan struct{}` parameter, select case fires `captureAndSend()` immediately
- **CaptureLoop stub:** Updated for signature compatibility
- **InputAck:** Agent sends `{type: "refresh_screenshot", detail: "refresh"}` back to frontend
- **Frontend trigger:** `handlePaletteAction('refresh-screenshot')` sends `{type: 'refresh_screenshot'}` via `sendDesktopInput`
- **Tests:** All passing (`go test ./internal/remoteapp/...` green)

## Return Shipments (Failed Gates)
None — all gates pass.

## Code Quality Findings
- Critical: 0
- Warning: 2 (both resolved in `c095be9e`)
- Suggestion: 1 (navigator.platform deprecation — non-blocking)
- Nit: 1 (eslint-disable for handlePaletteAction deps — non-blocking)

## Commits Reviewed
- `62b210e3`: feat(remoteapp): add command palette with meta key trigger and refresh screenshot
- `c095be9e`: fix(remoteapp): clear palette state on disconnect and restrict Ctrl trigger to non-macOS
