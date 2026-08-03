# PR #25 — Request Changes

**Readiness Score: 36/100** (Significant rework needed)
**Pre-Flight:** build=✅ tests=✅ (all pass with -race)
**Agents dispatched:** Architect, Logician, Sentinel, Performer, Cartographer, Observer
**Size:** Large (24 files, 1915 additions) — PR split recommended

## Critical Issues (MUST FIX)

### 1. `type_text` bypasses key whitelist — arbitrary keystroke injection
[internal/remoteapp/input.go#L102-L105](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/remoteapp/input.go#L102-L105) — *Flagged by: Sentinel*

**Problem:** `type_text` passes `event.Text` directly to `robotgo.Type()` with zero validation. Unlike `key_tap` (which checks `validKeys`), a malicious client can inject any keystrokes including OS shortcuts, passwords, or terminal commands. The entire `validKeys` whitelist is bypassed.

**Fix:** Validate text content — reject control characters and cap length:
```go
case "type_text":
    if len(event.Text) > 256 {
        return fmt.Errorf("type_text: text too long")
    }
    for _, r := range event.Text {
        if r < 0x20 || r == 0x7f {
            return fmt.Errorf("type_text: control character rejected")
        }
    }
    if event.Text != "" {
        robotgo.Type(event.Text)
    }
```

### 2. `handleRemoteAppUp` doesn't verify session ownership
[internal/server/remoteapp.go#L227-L276](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/remoteapp.go#L227-L276) — *Flagged by: Sentinel, Observer*

**Problem:** `handleRemoteAppUp` is wrapped with `userAuth` (authenticates the user), but only looks up the connect session by `X-SSET-Session` header without verifying the authenticated user owns that session. Any authenticated user who obtains a session ID can inject input events into another user's remote desktop.

**Fix:** Store `userID` in `connectSession` at creation and verify in `handleRemoteAppUp`:
```go
// In connectSession struct: add userID int64
// In handleRemoteApp creation: userID: sessInfo.UserID
// In handleRemoteAppUp after loading cs:
sessInfo := UserSessionFromContext(r)
if !isAdmin(sessInfo) && sessInfo.UserID != cs.userID {
    http.Error(w, "access denied", http.StatusForbidden)
    return
}
```

## Warnings (SHOULD FIX)

### 3. `readTypedFrame` duplicates `remoteapp.ReadFrame`
[internal/server/remoteapp.go#L281-L299](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/remoteapp.go#L281-L299) — *Flagged by: Architect, Logician, Cartographer*

**Problem:** Near-identical reimplementation of `remoteapp.ReadFrame`. Since `net.Conn` satisfies `io.Reader`, the existing function can be used directly.

**Fix:** Replace `readTypedFrame(stream)` with `remoteapp.ReadFrame(stream)` and remove the duplicate.

### 4. `WriteFrame` non-atomic two-write
[internal/remoteapp/protocol.go#L38-L53](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/remoteapp/protocol.go#L38-L53) — *Flagged by: Logician*

**Problem:** Two separate `w.Write()` calls (header + data). If a second writer is ever added to the same stream, frames would interleave and corrupt.

**Fix:** Combine into a single write:
```go
buf := make([]byte, 5+len(data))
buf[0] = frameType
binary.BigEndian.PutUint32(buf[1:5], uint32(len(data)))
copy(buf[5:], data)
_, err := w.Write(buf)
return err
```

### 5. `mouse_drag` stuck button — no recovery
[internal/remoteapp/input.go#L68-L77](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/remoteapp/input.go#L68-L77) — *Flagged by: Logician*

**Problem:** If a `down` event is received but the matching `up` is lost (network drop, tab close), the mouse button stays pressed indefinitely. No timeout, no session-end cleanup.

**Fix:** Track held button in `ProxyRemoteApp` and release on session teardown:
```go
defer func() {
    if dragButton != "" { robotgo.Toggle(dragButton, "up") }
}()
```

### 6. Bridge goroutine races with deferred `stream.Close()`
[internal/server/remoteapp.go#L139-L164](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/remoteapp.go#L139-L164) — *Flagged by: Performer*

**Problem:** The bridge goroutine may be partway through `stream.Write` when the deferred `stream.Close()` fires. Close `stream` after the bridge goroutine exits.

**Fix:** Use a done channel:
```go
bridgeDone := make(chan struct{})
go func() { defer close(bridgeDone); /* bridge loop */ }()
defer func() { cs.up.Close(); <-bridgeDone; stream.Close(); h.connectSessions.Delete(id) }()
```

### 7. Hot-path allocations: buffer + base64 per frame
[internal/remoteapp/capture.go#L41](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/remoteapp/capture.go#L41), [internal/server/remoteapp.go#L302-L308](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/remoteapp.go#L302-L308) — *Flagged by: Performer*

**Problem:** Fresh `bytes.Buffer` per frame (~150KB × 3 FPS = 450KB/sec) and `base64.EncodeToString` allocates ~200KB string per frame on server.

**Fix:** Reuse `bytes.Buffer` with `Reset()` in capture loop. Use `base64.NewEncoder` streaming into the response writer.

### 8. Zero tests for input dispatch + server handlers (478 lines)
[internal/remoteapp/input.go](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/remoteapp/input.go), [internal/server/remoteapp.go](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/remoteapp.go) — *Flagged by: Observer, Cartographer*

**Problem:** `DispatchInput` (key whitelist, coordinate clamping, button mapping) and `handleRemoteApp`/`handleRemoteAppUp` have zero test coverage. Protocol layer is well-tested but everything above it is not.

**Fix:** Add `input_test.go` (pure function tests for clampCoords, mapButton, sanitizeModifiers, key rejection) and `remoteapp_test.go` (readTypedFrame round-trip, handleRemoteAppUp error paths, SSE frame format).

### 9. Missing AGENTS.md for `internal/remoteapp/`
[internal/remoteapp/](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/remoteapp/) — *Flagged by: Cartographer*

**Problem:** Every other package has an AGENTS.md. The new package doesn't, and existing AGENTS.md files (server, agent) aren't updated.

**Fix:** Create `internal/remoteapp/AGENTS.md` and update `internal/server/AGENTS.md` and `internal/agent/AGENTS.md`.

### 10. Capture loop no circuit breaker on repeated failures
[internal/remoteapp/capture.go#L36-L44](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/remoteapp/capture.go#L36-L44) — *Flagged by: Observer*

**Problem:** If `robotgo.CaptureImg()` fails repeatedly (e.g., macOS permissions), the loop logs and continues indefinitely. Frontend shows frozen screen with no error.

**Fix:** Track consecutive failures; after threshold (e.g., 10), return error to tear down the session.

### 11. TOCTOU: ownership check before 3-second poll window
[internal/server/remoteapp.go#L43-L115](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/remoteapp.go#L43-L115) — *Flagged by: Sentinel*

**Problem:** `agentOwnedByUser` check at line 44 happens before the 3-second polling loop. During polling, the agent session could disconnect and a different user's session reconnect with the same agentID.

**Fix:** Re-verify ownership against the resolved `Session` after the polling loop.

### 12. `key_toggle` state not validated
[internal/remoteapp/input.go#L96-L100](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/remoteapp/input.go#L96-L100) — *Flagged by: Sentinel*

**Problem:** `robotgo.KeyToggle(key, state)` called with raw `event.State`. Only `"down"` and `"up"` are valid.

**Fix:** Validate state before calling:
```go
if state != "down" && state != "up" {
    return fmt.Errorf("invalid key_toggle state: %q", state)
}
```

### 13. SSE loop errors silently discarded
[internal/server/remoteapp.go#L186-L222](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/remoteapp.go#L186-L222) — *Flagged by: Observer*

**Problem:** SSE loop returns on non-timeout errors without logging. Production debugging is impossible.

**Fix:** Add `log.Printf` before return with agent ID, session ID, and error.

### 14. No metrics recorded for remoteapp sessions
[internal/server/remoteapp.go](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/remoteapp.go) — *Flagged by: Observer*

**Problem:** No `RecordSessionStart`/`RecordSessionEnd`, no downstream byte counting, no error recording. Remote desktop traffic is invisible to the metrics system.

**Fix:** Add session lifecycle metrics matching the shell/connect pattern.

## Suggestions (CONSIDER)

### 15. Use `remoteapp.WriteFrame` in handleRemoteAppUp instead of manual frame construction
[internal/server/remoteapp.go#L260-L263](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/remoteapp.go#L260-L263) — *Architect*

### 16. Handle `json.Marshal` error in ProxyRemoteApp
[internal/remoteapp/proxy.go#L30](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/remoteapp/proxy.go#L30) — *Logician, Observer*

### 17. Add session ID context to proxy logs
[internal/remoteapp/proxy.go](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/remoteapp/proxy.go) — *Observer*

### 18. Cap `mouse_scroll` amount (max ~20)
[internal/remoteapp/input.go#L55-L58](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/remoteapp/input.go#L55-L58) — *Sentinel*

### 19. Add rate limiting on input events per session
[internal/server/remoteapp.go#L227](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/remoteapp.go#L227) — *Performer*

### 20. Frontend: handle 401/409 in sendDesktopInput to auto-disconnect
[frontend/console/src/App.tsx#L540-L542](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/frontend/console/src/App.tsx#L540-L542) — *Observer*

### 21. Add `mouse_move` support in frontend (protocol supports it but frontend doesn't send)
[frontend/console/src/App.tsx#L635-L655](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/frontend/console/src/App.tsx#L635-L655) — *Architect*

### 22. Extract shared connect infrastructure from handleConnect/handleRemoteApp
[internal/server/remoteapp.go#L70-L223](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/remoteapp.go#L70-L223) — *Architect*

### 23. Frontend: extract Desktop panel component (duplicated ~82 lines admin/non-admin)
[frontend/console/src/App.tsx](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/frontend/console/src/App.tsx) — *Architect*

### 24. Add `X-SSET-Flags` guard on handleRemoteAppUp for API consistency
[internal/server/remoteapp.go#L227](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/remoteapp.go#L227) — *Architect*

## Nits / FYI

- **proxy.go:18** — `ProxyRemoteApp(stream net.Conn)` could accept `io.ReadWriteCloser` for narrower interface *(Architect)*
- **remoteapp.go:135** — Unused `resize` channel for remote app (acknowledged in comment) *(Architect)*
- **remoteapp.go:105** — `time.Sleep` in poll loop ignores context cancellation; use `select` *(Logician)*
- **protocol.go:16** — No extensibility strategy documented for frame types *(Architect)*

## Summary of Changes
- New `internal/remoteapp` package with typed length-prefixed wire protocol, build-tagged robotgo screen capture/input replay, and agent proxy
- Server-side frame-aware bridge: parses typed frames from yamux, emits base64 JPEG screenshots as default SSE data frames and screen info as named SSE events
- Console UI "Desktop" tab with live screenshot display, mouse click/scroll/context menu, keyboard input with modifier support and focus guard
- Coordinate scaling: browser viewport → agent screen dimensions
- Routes: `/console/api/v1/remoteapp/connect` (GET SSE) and `/connect-up` (POST)

## Dependency Impact
- New dependency on `github.com/go-vgo/robotgo` (behind `remoteapp` build tag, CI-safe)
- Server handler shares `connectSessions` sync.Map with shell/connect — session ownership validation gap affects all three
- Frontend adds ~360 lines to the already-large App.tsx (~2000 lines total)
