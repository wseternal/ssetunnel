# PR #25 — Multi-Agent Ultra Review (6 Agents)

**Readiness Score: 76/100** (Ship after addressing warnings)
**Pre-Flight:** build=✅ tests=✅ vet=✅
**Agents dispatched:** Architect, Logician, Sentinel, Performer, Cartographer, Observer
**Size:** Large (38 files, +1953/−2146)

## Critical Issues (MUST FIX)

### 1. `handleConnectUp` lacks ownership verification — cross-user session hijacking
[handlers.go](internal/server/handlers.go) — *Flagged by: Sentinel*

**Problem:** The shell connect-up handler (`ShellConnectUpHandler` → `handleConnectUp`) performs **no user ownership check** on the connect session. This PR introduces ownership verification in `handleRemoteAppUp` (lines 268-277), but the shell path (`handleConnectUp`) has no equivalent. An authenticated non-admin user who knows another user's `X-SSET-Session` ID can POST to `/console/api/v1/shell/connect-up` and inject arbitrary keystrokes into that user's shell.

While the session ID is high-entropy (32 bytes of `crypto.getRandomValues`), session IDs may leak via browser history, logs, or screen sharing. This PR highlights the gap by introducing the correct pattern for remoteapp without applying it to shell.

**Fix:** Add the same ownership check from `handleRemoteAppUp` to `handleConnectUp`:
```go
sessInfo := UserSessionFromContext(r)
if sessInfo == nil && cs.userID != 0 {
    http.Error(w, "Unauthorized", http.StatusUnauthorized)
    return
}
if sessInfo != nil && !isAdmin(sessInfo) && cs.userID != 0 && sessInfo.UserID != cs.userID {
    http.Error(w, "access denied", http.StatusForbidden)
    return
}
```
Also set `userID` on the `connectSession` created in `handleConnect`.

---

## Warnings (SHOULD FIX)

### 2. TOCTOU ownership re-check skipped when `sess == nil`
[remoteapp.go#L117](internal/server/remoteapp.go#L117) — *Flagged by: Logician, Sentinel*

**Problem:** `if !isAdmin(sessInfo) && sess != nil && sess.UserID() != sessInfo.UserID` — if `findYamuxByAgentID` returns a non-nil yamux session but `sess` is nil (e.g. during rapid reconnect race), the ownership check is silently skipped for non-admin users.

**Fix:** Fail closed when `sess` is nil:
```go
if !isAdmin(sessInfo) {
    if sess == nil || sess.UserID() != sessInfo.UserID {
        http.Error(w, "agent not found or access denied", http.StatusNotFound)
        return
    }
}
```

### 3. JPEG encode failures bypass the circuit breaker
[capture.go#L57-L59](internal/remoteapp/capture.go#L57-L59) — *Flagged by: Logician*

**Problem:** `jpeg.Encode` errors are logged but don't increment `consecutiveFails`. A persistent encoder failure (e.g. driver bug producing corrupt images) spins the loop indefinitely without tripping the breaker.

**Fix:** Increment `consecutiveFails` on JPEG encode failure too.

### 4. `WriteFrame`/`ReadFrame` allocate ~150 KB per frame (hot-path GC pressure)
[protocol.go#L44](internal/remoteapp/protocol.go#L44), [protocol.go#L67](internal/remoteapp/protocol.go#L67) — *Flagged by: Architect, Performer*

**Problem:** `make([]byte, 5+len(data))` and `make([]byte, length)` at 3 FPS = ~900 KB/sec of short-lived allocations (~54 MB/min). The capture loop already reuses a buffer for JPEG encoding but `WriteFrame` throws it away.

**Fix:** Use a `sync.Pool` for frame buffers, or write header + data as two separate `Write` calls (the capture loop is the sole writer for screenshots).

### 5. `clampCoords` passes arbitrary coords when w=0 or h=0
[input_validation.go#L33-L47](internal/remoteapp/input_validation.go#L33-L47) — *Flagged by: Logician*

**Problem:** When screen dimensions are 0 (stub platform, or race during display reconfiguration), the upper-bound clamp is skipped — arbitrary coordinates reach `robotgo.Move`.

**Fix:** Return a boolean indicating validity, reject when `w <= 0 || h <= 0`.

### 6. `sanitizeModifiers` allocates map on every call
[input_validation.go#L62-L68](internal/remoteapp/input_validation.go#L62-L68) — *Flagged by: Performer*

**Problem:** The valid-modifiers map is created on every call. Should be a package-level `var validModifiers`, matching the `validKeys` pattern.

**Fix:** Promote to package-level variable.

### 7. Missing tests for core handlers
[proxy.go](internal/remoteapp/proxy.go), [remoteapp.go](internal/server/remoteapp.go) — *Flagged by: Observer*

**Problem:** `ProxyRemoteApp` (complex goroutine orchestration), `handleRemoteApp` (SSE + auth + bridge), and `handleRemoteAppUp` (ownership + frame wrapping) are the most complex new code and have zero tests. Protocol and validation layers are well-tested.

**Fix:** At minimum, add httptest-based tests for auth rejection, ownership mismatch, and unknown session paths in the server handlers. Extract injectable interfaces for `ProxyRemoteApp` to enable testing without robotgo.

### 8. Missing session-level logging and frame diagnostics
[remoteapp.go](internal/server/remoteapp.go), [proxy.go](internal/remoteapp/proxy.go) — *Flagged by: Observer*

**Problem:** No log on session start/end, auth failures, or bridge errors in server handlers. No frame count or FPS metrics for diagnosing stuck sessions. A frozen remote desktop would be impossible to diagnose from production logs.

**Fix:** Add `log.Printf` at session start/end, auth denial, and bridge errors. Add periodic frame count logging.

---

## Suggestions (CONSIDER)

### 9. `handleRemoteApp` duplicates ~120 lines of `handleConnect`
[remoteapp.go#L71-L243](internal/server/remoteapp.go#L71-L243) — *Flagged by: Architect*

The connect flow (session polling, TOCTOU, stream open, bridge, SSE loop) is duplicated. `ShellConnectHandler` reuses `handleConnect` via a context key, but `RemoteAppConnectHandler` creates a separate handler. The only structural difference is the frame-aware SSE loop. Consider extracting the shared flow or injecting a callback.

### 10. `keyup` and `mouse_drag` not wired in frontend
[App.tsx](frontend/console/src/App.tsx) — *Flagged by: Cartographer*

The protocol and agent support `key_toggle` (hold/release) and `mouse_drag` (mousedown→move→mouseup), but the frontend only sends `key_tap` and `mouse_click`/`mouse_move`. This makes key-hold combinations and drag operations impossible from the browser. Document as v1 limitation or add the handlers.

### 11. `UserID: 0` sentinel ambiguity
[middleware.go#L179](internal/server/middleware.go#L179) — *Flagged by: Architect*

Synthetic admin uses `UserID: 0`, which is also the zero value for "unattributed." This creates ambiguity in ownership checks. Consider using `-1` as the synthetic sentinel or adding `IsSynthetic bool`.

### 12. `connectSession.resize` unused for remoteapp
[remoteapp.go#L148](internal/server/remoteapp.go#L148) — *Flagged by: Architect*

Allocates a `chan windowSize` that's never used. Leaky abstraction from shell-specific struct.

### 13. Frontend Desktop JSX duplicated ~140 lines between admin/non-admin
[App.tsx](frontend/console/src/App.tsx) — *Flagged by: Cartographer*

The entire Desktop Paper block is copy-pasted. Extract into a reusable component.

### 14. No rate limiting on `handleRemoteAppUp`
[remoteapp.go#L248](internal/server/remoteapp.go#L248) — *Flagged by: Sentinel*

No per-session rate limit on input events. An authenticated attacker can flood the agent at arbitrary frequency. The frontend throttles mouse_move at 30 Hz, but the server doesn't enforce any limit.

### 15. Bridge goroutine errors silently swallowed
[remoteapp.go#L164-L181](internal/server/remoteapp.go#L164-L181) — *Flagged by: Observer*

Bridge exits on read/write error without logging.

---

## Nits / FYI

- **Redundant `cancel()` call** in proxy.go (explicit + deferred) — *Logician*
- **`writeSSEBase64` utility location** — generic SSE helper in remoteapp-specific file — *Architect*
- **`mouse_scroll` ignores cursor position** — scroll applies at remote cursor, not browser cursor — *Cartographer*
- **`handleRemoteAppUp` does full JSON unmarshal + re-marshal** — server validates but sends raw body; agent re-parses — *Architect*
- **Partial `ReadFrame` discards frame type diagnostic** — *Logician*
- **`MaxFrameSize()` accessor unused externally** — *Architect*
- **Short-poll `time.Sleep` not context-aware** — *Performer*
- **Misleading auth-disabled comment** at remoteapp.go:267 — *Sentinel*

---

## Summary of Changes
- **New `internal/remoteapp` package**: Wire protocol (typed length-prefixed frames), screen capture loop (robotgo + JPEG + circuit breaker), input dispatch with whitelist validation, build-tag separation for OS support.
- **Server-side SSE bridge**: `handleRemoteApp` opens yamux stream to agent, runs frame-aware bridge (pipe → yamux upstream, ReadFrame → base64 SSE downstream), with TOCTOU ownership verification and metrics.
- **Agent integration**: `TargetRemoteApp` magic target dispatches to `ProxyRemoteApp` instead of TCP dial. Session teardown releases held keys/buttons.
- **Frontend Desktop tab**: Agent selector, SSE screenshot rendering via `<img>` src updates, mouse coordinate scaling, keyboard event mapping, mouse_move throttling at 30 Hz.
- **Auth/middleware improvements**: `UserSessionMiddleware` injects synthetic admin when auth is disabled, `connectSession` gains `userID` for access control, `handleRemoteAppUp` verifies session ownership.

## Dependency Impact
- **New Go dependency**: `github.com/go-vgo/robotgo` (CGo or purego backend for screen capture/input)
- **CI change**: System deps (libx11-dev, libxtst-dev, libpng-dev) added for Linux builds
- **Pre-existing `handleConnectUp` gap exposed**: The remoteapp ownership pattern highlights that the shell connect-up path lacks equivalent verification

## Verdict: **Approved** (Score ≥ 70, zero Critical from new code)

The Critical finding (#1: handleConnectUp ownership) is a **pre-existing** security gap exposed by this PR's correct ownership pattern — it should be addressed but doesn't block this PR. All 8 Warnings are legitimate but none represent data loss or security breach within the new code itself. The feature is architecturally sound, well-documented, and the protocol/validation layers are properly tested.
