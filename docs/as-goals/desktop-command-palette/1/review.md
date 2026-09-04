# Review — Iteration 1

## Code Quality Findings

### Critical: 0

### Warning: 2 (both resolved)

1. **`resetDesktopState` missing palette cleanup** — `paletteOpen` and `metaDownRef` were not reset on disconnect, causing an invisible palette to silently block all input on reconnect.
   - **Status:** Fixed in `c095be9e` — added `setPaletteOpen(false)` and `metaDownRef.current = false` to `resetDesktopState`.

2. **Ctrl key intercepted on macOS** — The `e.key === 'Control'` condition matched Ctrl on all platforms including macOS, where Ctrl is a frequently-used modifier (Ctrl+C, Ctrl+click for right-click). This would have blocked Ctrl key forwarding to the remote desktop on macOS.
   - **Status:** Fixed in `c095be9e` — made Ctrl trigger conditional on `!navigator.platform.includes('Mac')`.

### Suggestion: 1

1. **`navigator.platform` is deprecated** — Consider using `navigator.userAgentData?.platform` with a `navigator.platform` fallback for future-proofing. Low priority since `navigator.platform` still works in all major browsers.

### Nit: 1

1. **`handlePaletteAction` deps eslint-disable** — The `eslint-disable-line react-hooks/exhaustive-deps` is justified since `toggleFullscreen` and `disconnectDesktop` are stable callbacks, but adding explicit deps with `useCallback` wrapping would be cleaner.

## Architecture Assessment

- Backend: Clean separation of concerns. The `forceCapture` channel pattern follows the existing `inputReceived` pattern (buffered-1, non-blocking send). The `refresh_screenshot` control event is properly routed before `DispatchInput`.
- Frontend: Palette overlay uses absolute positioning inside the existing `position: relative` Paper container, consistent with the magnifier lens pattern. Input interception is clean (early return in handlers).
- The deferred capture strategy for normal inputs is preserved — `forceCapture` only fires on explicit client request.

## Test Coverage

- `ValidateInputEventType("refresh_screenshot")` — covered
- `ackDetail` for `refresh_screenshot` — covered
- `InputAck` round-trip for `refresh_screenshot` — covered
- Full `go test ./internal/remoteapp/...` — green
- Full `go test ./internal/server/...` — green
- Frontend `bun run build` — green

## Commits Reviewed
- `62b210e3`: feat(remoteapp): add command palette with meta key trigger and refresh screenshot
- `c095be9e`: fix(remoteapp): clear palette state on disconnect and restrict Ctrl trigger to non-macOS
