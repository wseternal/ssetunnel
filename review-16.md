# PR #16 Review - Request Changes

## Critical Issues (MUST FIX)

### 1. `__shell__` target rejected by agent config validation when auth is enabled
[shell.go#L43-L48](internal/server/shell.go), [handlers.go#L408-L429](internal/server/handlers.go)

**Problem:** `ShellConnectHandler` forces `target=__shell__` in the query parameters before delegating to `handleConnect`. Inside `handleConnect` (line 413), when auth is enabled (`h.store != nil`) and `agentID` is set, the server validates the target against the agent's `allowed_targets` config:
```go
if target != "" && h.store != nil {
    cfg, err := h.store.GetAgentConfig(ctx, agentID)
    if !auth.TargetAllowed(cfg.AllowedTargets, target) {
        http.Error(w, fmt.Sprintf("target %q not allowed", target), http.StatusForbidden)
    }
}
```
`TargetAllowed` parses `__shell__` as a host:port — `net.SplitHostPort("__shell__")` fails, so it treats it as host `__shell__` with empty port. Unless the agent config has `*` (wildcard) or explicitly lists `__shell__`, the validation returns 403. In any production deployment with specific allowed targets (e.g., `127.0.0.1:22, 127.0.0.1:3306`), cloud shell is completely broken.

**Fix:** `ShellConnectHandler` should bypass target validation since `__shell__` is a synthetic target. The cleanest approach: clear `target` from the query before calling `handleConnect` (so the validation block is skipped), and inject `__shell__` into the yamux stream header via a context key or by modifying `handleConnect` to accept an override. For example:
```go
q.Set("target", "")  // skip validation
r.URL.RawQuery = q.Encode()
// Pass forced target via context for handleConnect to use when writing stream header
ctx := context.WithValue(r.Context(), forcedTargetKey, TargetShell)
h.handleConnect(w, r.WithContext(ctx))
```
Then in `handleConnect`, when writing the target header:
```go
targetHeader := target
if ft, ok := r.Context().Value(forcedTargetKey).(string); ok && ft != "" {
    targetHeader = ft
}
if targetHeader != "" && sess != nil && sess.WantTarget() {
    fmt.Fprintf(stream, "%s\n", targetHeader)
}
```

## Warnings (SHOULD FIX)

### 2. PTY window size never updated — full-screen apps render incorrectly
[shell_unix.go#L31](internal/agent/shell_unix.go), [App.tsx#L346-L359](frontend/console/src/App.tsx)

**Problem:** The PTY is created with a default 80×24 window. When the user resizes the browser terminal (xterm.js FitAddon adjusts columns/rows), the PTY dimensions are never updated. Full-screen applications (vim, less, htop, top) render incorrectly because they read the terminal size from the PTY, not from the browser.

**Fix:** This requires a resize signaling path:
1. Frontend: `term.onResize(({cols, rows}) => sendResize(cols, rows))` — POST dimensions to a new endpoint or encode in a special upstream frame
2. Server: receive resize request and forward to agent via yamux stream control frame or dedicated stream
3. Agent: call `pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})`

This is a significant feature that can be a follow-up PR, but it should at minimum be tracked as a known limitation.

### 3. Frontend xterm input handler leaks on reconnect
[App.tsx#L324-L334](frontend/console/src/App.tsx), [App.tsx#L396](frontend/console/src/App.tsx)

**Problem:** `connectShell` registers `term.onData(sendInput)` at line 396. The returned `inputDisposable` is only disposed in the `finally` block of the SSE read loop. When `disconnectShell` aborts the connection (line 324-334), it does NOT dispose the input handler. If the user reconnects, a new `onData` handler is registered while the old one persists — keystrokes are sent twice (or more with each reconnect cycle).

**Fix:** Track the disposable in a ref and dispose it in `disconnectShell`:
```typescript
const inputDisposableRef = useRef<IDisposable | null>(null);
// In connectShell: inputDisposableRef.current = term.onData(sendInput);
// In disconnectShell: inputDisposableRef.current?.dispose(); inputDisposableRef.current = null;
```

### 4. Session token exposed in SSE URL query string
[App.tsx#L375](frontend/console/src/App.tsx)

**Problem:** The session token is included in the SSE URL: `?token=${encodeURIComponent(sessionToken)}`. This token appears in browser history, server access logs, and any proxy logs. While `ConnectAuthMiddleware` does support query token auth (`ExtractBearerTokenWithQuery`), this is a security concern for production deployments.

**Fix:** Use the `Authorization` header instead (fetch supports custom headers, unlike EventSource):
```typescript
const resp = await fetch(sseURL, {
  headers: { Authorization: `Bearer ${sessionToken}` },
  signal: abort.signal,
});
```
And remove `&token=...` from the URL.

## Suggestions (CONSIDER)

### 5. Duplicate shell UI code for admin and non-admin tabs
[App.tsx#L934-L1207](frontend/console/src/App.tsx) and [App.tsx#L1208-L1420](frontend/console/src/App.tsx)

The shell tab JSX is nearly identical between the admin section (tabIndex === 4) and non-admin section (tabIndex === 3). The only difference is the agent selector data source (`agentMetrics` vs `agents`). Consider extracting a shared `ShellPanel` component.

### 6. `ShellConnectUpHandler` has redundant UserSessionMiddleware
[consoleserver.go#L41](internal/consoleserver/consoleserver.go)

The `userAuth` middleware on `/console/api/v1/shell/connect-up` validates the user session, but `handleConnectUp` internally uses connect-session auth via `X-SSET-Session` header. The outer userAuth is not harmful but adds redundant DB queries. This is consistent with the other shell route, so it's a minor nit.

### 7. `proxyShell` on Agent receiver but doesn't use `a`
[shell_unix.go#L19](internal/agent/shell_unix.go), [shell_windows.go#L12](internal/agent/shell_windows.go)

Both implementations receive `*Agent` but don't access any of its fields. Consider making it a package-level function or documenting that future versions may use agent config (e.g., custom shell path).

## Summary of Changes
- **Agent-side PTY shell**: New `proxyShell` function spawns an interactive shell with PTY (creack/pty) for `__shell__` target; Unix implementation with bidirectional io.Copy + WaitGroup for clean teardown; Windows stub.
- **Server-side shell routing**: `ShellConnectHandler` wraps `/connect` with user-scoped access control and forced `target=__shell__`; `ShellConnectUpHandler` delegates to `/connect-up`; routes mounted on console server with user session auth.
- **Console WriteTimeout=0**: Console HTTP server disables write timeout to support indefinite SSE streams for cloud shell.
- **Frontend xterm.js terminal**: Shell tab with xterm.js 6.0 terminal, SSE downstream via fetch ReadableStream with base64 decoding, POST upstream for keystrokes; separate implementations for admin and non-admin users.
- **Tests**: Integration tests for both fixed-target and dynamic-target shell dispatch modes using real TCP connections and PTY.
