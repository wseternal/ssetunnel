# Plan — Iteration 1

## Goal
Remove the deferred capture mechanism entirely, keep only initial capture + manual refresh, and move the Activity Log to a top-left collapsible overlay.

## Task 1: Remove deferred capture from `capture.go`

**File:** `internal/remoteapp/capture.go`

**Changes:**
1. Remove the `deferDelay` constant (line 31)
2. Remove the `inputReceived <-chan struct{}` parameter from `CaptureLoop` signature (line 58)
3. Remove the `drainTimer` helper function (lines 122-129)
4. Remove the `deferTimer` variable and all timer logic (lines 153-165, 181-187, 200-225)
5. Remove the `case <-inputReceived:` branch (lines 196-202)
6. Remove the `case <-deferTimer.C:` branch (lines 203-225)
7. Remove the `backoffDeadline` variable and all references (lines 143, 148-151, 154-156, 180-187, 201, 206, 213-224)
8. Update the doc comment to remove deferred-capture description
9. Update the `writeLog` call on line 139 to remove "deferred" wording
10. Simplify the loop to only handle: `ctx.Done()` and `forceCapture`

**Result:** `CaptureLoop` becomes: initial capture → loop waiting for `ctx.Done()` or `forceCapture`. On `forceCapture`, capture immediately. No timers, no backoff scheduling, no input-received handling.

**Acceptance criteria:**
- `go build ./internal/remoteapp/` compiles
- No references to `deferDelay`, `deferTimer`, `inputReceived`, `backoffDeadline`, `drainTimer` remain in capture.go
- The display-unavailable transient handling in `captureAndSend` still works (returns `transient=true`)
- On `forceCapture` with transient display-unavailable, log a warning but do NOT schedule a retry (no backoff timer)

## Task 2: Remove `inputReceived` from `proxy.go`

**File:** `internal/remoteapp/proxy.go`

**Changes:**
1. Remove the `inputReceived` channel creation (line 67)
2. Remove the `signalInput` function (lines 94-99)
3. Remove the `signalInput()` call in the main loop (line 153)
4. Update the `CaptureLoop` call to remove the `inputReceived` argument (line 84)
5. Update the doc comment to remove deferred-capture description (lines 19-22)

**Acceptance criteria:**
- `go build ./internal/remoteapp/` compiles
- No references to `inputReceived` or `signalInput` remain in proxy.go

## Task 3: Remove `inputReceived` from `capture_stub.go`

**File:** `internal/remoteapp/capture_stub.go`

**Changes:**
1. Remove the `inputReceived <-chan struct{}` parameter from the stub `CaptureLoop` signature (line 11)

**Acceptance criteria:**
- `go build -tags purego ./internal/remoteapp/` compiles

## Task 4: Update tests in `capture_test.go`

**File:** `internal/remoteapp/capture_test.go`

**Changes:**
1. Keep `TestForceCaptureChannelCoalescing` — still valid (forceCapture channel pattern unchanged)
2. Keep `TestForceCapturePriorityOverDeferTimer` but rename to `TestForceCapturePriorityPreCheck` — the priority pre-check pattern still exists but there's no defer timer anymore; update the test to verify forceCapture is drained by the pre-check before the main select sees ctx.Done
3. Keep `TestForceCapturePriorityDuringBackoff` — still valid (forceCapture bypasses any state)

**Acceptance criteria:**
- `go test ./internal/remoteapp/ -v -run Force` passes

## Task 5: Move Activity Log to top-left overlay in `App.tsx`

**File:** `frontend/console/src/App.tsx`

**Changes:**
1. Add state: `const [desktopLogCollapsed, setDesktopLogCollapsed] = useState(false);`
2. Remove the entire bottom Activity Log block (lines 2305-2339)
3. Add a new overlay inside the desktop viewer Paper (the one with `position: 'relative'` at line 1997), positioned at `top: 8, left: 8, zIndex: 12`
4. The overlay shows:
   - A small header bar with "Activity Log" text and a collapse/expand toggle button (chevron icon)
   - When expanded: the log entries list (same rendering as before, semi-transparent dark background)
   - When collapsed: just the header bar
5. Use `position: 'absolute'` positioning within the relative container
6. Semi-transparent background: `bgcolor: 'rgba(30, 30, 46, 0.85)'`
7. Max width constraint (e.g., 400px) so it doesn't cover the whole screen
8. Keep the `desktopLogRef` for auto-scroll
9. Keep the same log entry rendering (timestamp, severity, source, message)

**Acceptance criteria:**
- `cd frontend/console && npx vite build` succeeds
- Activity Log appears as overlay in top-left of desktop viewer
- Toggle button collapses/expands the log entries
- Auto-scroll still works
- Log entries have same format as before

## Task 6: Update AGENTS.md documentation

**File:** `internal/remoteapp/AGENTS.md`

**Changes:**
1. Remove all references to deferred capture, `inputReceived`, 3-second timer
2. Update `CaptureLoop` signature documentation
3. Update the capture strategy description to reflect: initial capture + force capture only
4. Update `ProxyRemoteApp` flow description

**Acceptance criteria:**
- Documentation accurately describes the new behavior

## Execution Order

1. Task 1 (capture.go) — core change
2. Task 2 (proxy.go) — depends on Task 1 signature change
3. Task 3 (capture_stub.go) — depends on Task 1 signature change
4. Task 4 (capture_test.go) — depends on Task 1
5. Task 5 (App.tsx) — independent of Tasks 1-4
6. Task 6 (AGENTS.md) — depends on Tasks 1-3 completion

## Verification

```bash
go build ./...
go vet ./...
go test ./internal/remoteapp/... -v
cd frontend/console && npx vite build
```
