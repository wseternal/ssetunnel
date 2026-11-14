# Browser Client Server

Secured local HTTP server behind `cli ui`. Serves the embedded client SPA (`frontend/client` package) and a JSON API delegating to `internal/clientapp`. Implements the ADR-005 browser security contract.

## Security Contract (enforced)

- Loopback-only bind: `validateLoopbackListen` rejects non-loopback `--listen` before the listener opens. The Host check is NOT a compensating control — non-browser clients control their Host header.
- 32-byte random launch secret per invocation, delivered in the URL fragment; constant-time Bearer comparison on every `/api` request.
- Exact Host check (`127.0.0.1`, `localhost`, `::1`) via `net.SplitHostPort` (bracketed IPv6 safe). Origin check: http scheme + loopback hostname + matching port.
- `securityHeaders` wraps the whole router (above mux) so every response class — including mux 404/405/301 — carries CSP `default-src 'self'`, `nosniff`, `DENY`, `no-referrer`, `no-store`. Handlers may override individual values.
- The SPA is served by dedicated handlers, NOT litespaserver: `serveIndex` (document read once at startup) and `serveAsset` (content-hashed, `immutable`, ETag/304, containment-checked). litespaserver `Set()`s its own headers over outer wrappers and puts `no-store` on hashed assets.
- 64 KiB JSON body limit (413), server timeouts, `DisableGeneralOptionsHandler` (OPTIONS * answered inside the header contract).
- No passwords/tokens in DTOs; login errors are status-only; `uiError` translates CLI-shaped workflow errors, strips control characters, caps length.

## Lifecycle (runtime.go)

- `agentRuntime`: one agent at a time. Per-run `agentRun{cfg, cancel, done, err atomic.Pointer[error]}`; `opMu` serializes start/stop transitions, `mu` guards only the current-run pointer. The goroutine stores its error then closes `done` — it never takes a lifecycle lock (the shared-field design deadlocked: start waited on done while holding the mutex the goroutine needed). Waits are bounded by `teardownTimeout` (20 s; worst case is the 15 s non-cancellable refresh). `stop` clears the current run only if it is still the captured one (generation guard). `status` snapshots and never blocks on teardown.
- `connectRegistry`: named instances, duplicate-name rejection, `errNotFound` sentinel (404) vs run errors (successful stop), parallel bounded `stopAll`.
- Shutdown: `srv.Shutdown` first (no new registrations during teardown), then parallel agent/connect teardown; `Serve` gates its return on teardown completion when the context was cancelled.
- Logout stops owned runtimes before deleting the session entry (ADR-005 ownership).

## Rules

* No streaming responses here — the `statusWriter` wrapper is safe in this package only. Never copy it to `consoleserver` (SSE handlers assert `http.Flusher` on the original writer).
* Route order: `/api` subrouter (auth) → `/assets/` → bare `/assets` 404 → SPA catch-all last.
* The launch secret comes from `URL()`'s fragment; there is no secret override knob.
* Regression tests (`runtime_test.go`, `server_test.go`) use the real `Serve` boundary and an httptest tunnel stub; the test HOME must be short (macOS unix-socket path limit).