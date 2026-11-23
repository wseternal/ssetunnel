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

- `agentRuntime`: one agent at a time. Per-run `agentRun{cfg, cancel, done, err atomic.Pointer[error]}`; `opMu` serializes start/stop transitions, `mu` guards only the current-run pointer. The goroutine stores its error then closes `done` — it never takes a lifecycle lock (the shared-field design deadlocked: start waited on done while holding the mutex the goroutine needed). Waits are bounded by `teardownTimeout` (20 s; worst case is the 15 s non-cancellable refresh). `stop` clears the current run only if it is still the captured one (generation guard). `status` snapshots and never blocks on teardown. When `ar.cur` is nil, `status()` falls back to `clientapp.ReadExternalAgentStatus()` to detect agents running outside the UI process (e.g. `cli agent run` or OS services), returning `External: true` with the external agent's PID. `start()` terminates any detected external agent before starting a new in-process one to prevent dual-agent conflicts.
- `connectRegistry`: named instances, duplicate-name rejection, `errNotFound` sentinel (404) vs run errors (successful stop), parallel bounded `stopAll`.
- Shutdown: `srv.Shutdown` first (no new registrations during teardown), then parallel agent/connect teardown; `Serve` gates its return on teardown completion when the context was cancelled.
- Logout stops owned runtimes before deleting the session entry (ADR-005 ownership).

## Module Proxy

`/api/v1/modules/**` is a proxy, not a reimplementation. `module_proxy.go` forwards every request to the loopback module API the agent published, so the browser never talks to the agent and the shared module policy lives in one place (`internal/moduleapi`).

- Target: `clientapp.ReadExternalAgentStatus()` → `module_api_url`, validated per request (scheme `http`, no userinfo, host must be loopback, port required, no path/query/fragment). Anything else is `503` — an owned run publishes this only after its module subsystem starts, so a stale or missing endpoint must fail loudly instead of being cached.
- Transport: dedicated `http.Transport{Proxy: nil, DisableCompression: true}` and a client that does not follow redirects. A proxy inherited from the environment would send tunnel-only machines somewhere else; transparent gzip plus a relayed body without `Content-Encoding` would corrupt output.
- Method: `moduleapi.ResolveMethod` turns `POST` + `X-SSET-Method-Override: TRACE` into TRACE.
- Request: credentials, hop-by-hop, forwarding, browser Fetch Metadata, the override header, and `Accept-Encoding` are stripped before forwarding.
- Response: `moduleapi.CopyResponseHeaders` (allowlist, no `cache-control`), then the body is streamed with a 32 KiB flush cadence so a long module response renders as it arrives. Errors surface as `502` (upstream failure) or `503` (no usable endpoint).

## Rules

* `statusWriter` forwards `Flush` and `Unwrap` (for `http.ResponseController`) because the module proxy streams. It is still package-local: never copy it to `consoleserver`, whose SSE handlers assert `http.Flusher` on the original writer.
* The client server's `WriteTimeout` (30 s) bounds forwarded module responses; the proxy cannot outlive it.
* Route order: `/api` subrouter (auth) → `/assets/` → bare `/assets` 404 → SPA catch-all last.
* The launch secret comes from `URL()`'s fragment; there is no secret override knob.
* Regression tests (`runtime_test.go`, `server_test.go`) use the real `Serve` boundary and an httptest tunnel stub; the test HOME must be short (macOS unix-socket path limit).

## Known Limitations (deferred)

* `ServeListener` does not WaitGroup-drain active proxy goroutines before returning; on restart, old forwarded sessions overlap the new generation briefly until the transport close unwinds their copies (no permanent leak).
* `ResolveSession` reads the session file 2–3× per call (load + entry + refresh re-read); cold path, small file — a single-read refactor would churn `auth.LoadSession`'s signature across all consumers.
* The 20 s teardown cap can be exceeded by `Supervisor.Stop` with several enabled modules (serial 5 s/module); the stop then reports a timeout but state stays consistent and a retry succeeds.