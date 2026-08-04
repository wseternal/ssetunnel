# PR #25 Re-Review — Approved

## Summary of Commit 6ada71c

- **WriteFrame zero-alloc:** sync.Pool for 5-byte headers + two-write pattern (0 allocs/op confirmed by benchmark)
- **ReadFrameInto:** New zero-alloc variant reads into caller-supplied buffer; SSE loop uses 4 MiB reusable buffer
- **handleRemoteAppUp tests:** 10 httptest-based tests covering all code paths (method, auth, ownership, flags, JSON, type, happy path)
- **Server-side InputEvent.Type validation:** Whitelist of 7 valid types; rejects unknown with HTTP 400
- **mouse_scroll:** Moves cursor to (x,y) before ScrollDir; frontend includes x/y in wheel payload
- **mouse_drag:** Validates state is "down" or "up" before processing
- **connectSession.resize:** Set to nil for remoteapp (nil-channel is safe in select)
- **Desktop JSX dedup:** Extracted shared `desktopPanel` variable (~140 lines dedup)

## Findings

### Warnings (SHOULD FIX)

#### mouse_drag: Move cursor before Toggle for drag-start
[internal/remoteapp/input.go:47-58](internal/remoteapp/input.go)
**Problem:** When `state == "down"`, the code calls `robotgo.Toggle(btn, "down")` at the cursor's current position, then moves to `(x, y)`. This creates an unintended drag from the old position. The correct sequence is: **move first, then press**.
**Fix:** Reorder to Move → Toggle:
```go
case "mouse_drag":
    // validate state...
    x, y, ok := clampCoords(...)
    robotgo.Move(x, y)
    if event.State == "down" {
        robotgo.Toggle(btn, "down")
    }
    if event.State == "up" {
        robotgo.Toggle(btn, "up")
    }
```
*Note: Frontend doesn't currently send mouse_drag events, so this is latent — won't affect current users.*

### Nits / FYI

#### 4 MiB readBuf per session — acceptable
[internal/server/remoteapp.go:210](internal/server/remoteapp.go)
Allocates 4 MiB heap buffer per active session. For typical deployments (< 100 concurrent desktop sessions), this is fine. Could use sync.Pool if thousands of sessions are expected.

#### ReadFrameInto error text could be clearer
[internal/remoteapp/protocol.go:107](internal/remoteapp/protocol.go)
`"buffer too small (256 < 1024)"` wraps `ErrFrameTooLarge` but the issue is the caller's buffer, not the frame size. Consider a separate error type or clearer text.

#### mouse_scroll moves cursor to (0,0) if x/y omitted
[internal/remoteapp/input.go:33-40](internal/remoteapp/input.go)
If a client omits x/y (JSON zero values → 0,0), cursor moves to top-left before scrolling. Current frontend always sends x/y. Noted for API backward compat with third-party clients.

## Verdict

**Approved** — All stated items correctly implemented. No critical issues. One Warning (mouse_drag ordering) is a latent bug that won't affect current users but should be fixed for future-proofing.
