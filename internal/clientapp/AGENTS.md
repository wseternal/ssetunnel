# Client Workflows

Transport-neutral client workflows shared by every surface: CLI foreground, CLI service, browser API (`internal/clientserver`), and (once wired) the Clix desktop app. No HTTP-handler or flag-parsing code here.

## API

- `Login(ctx, LoginParams) (*LoginResult, error)` — posts to the console `/user-login`, saves the session, returns redacted metadata. Bounded 15 s client; cross-host redirects refused; console URL validated with `auth.ValidateConsoleURL` (HTTPS for non-loopback). Errors carry status only — never the remote body. Sentinels: `ErrInvalidCredentials` (401/403 from console), `ErrConsoleUnavailable` (network/other). `LoginResult.Warning` is set when the tunnel URL could not be derived.
- `CheckTOTPRequired(ctx, consoleURL, username) (bool, error)` — errors are propagated; the CLI fails open (optional code prompt), the browser API returns 502.
- `ResolveSession(serverURL, prefix) (*SessionInfo, error)` — loads the saved session (first entry when `serverURL` empty), proactively refreshes near-expiry tokens, tolerates refresh by another process, hard-fails on expired-and-unrefreshable.
- `NewConsoleClient(serverURL) (*ConsoleClient, error)` — resolves the saved session, validates the console URL with `auth.ValidateConsoleURL`, and builds `<console>/api/v1`. An explicit `serverURL` is the console base only and never becomes the session's stored server URL, so a refresh writes the tunnel endpoint back and a host with no saved entry stays unauthenticated. A missing token is allowed (auth-disabled dev servers); the server then answers 401. Methods: `ListAgents`, `ListModules`, `Operations`, `OpenAPI`, `Invoke`. The shared transport has a 10 s dial timeout, a 60 s response-header timeout, no total timeout (bodies stream), and refuses redirects so a bearer token never follows `Location`.
- `RunAgent(ctx, AgentConfig) error` — session resolution, token-refresh callback (mutex-guarded shared token), module init, `agent.Run`. Defers `agent.Close()` (module IPC + supervisor teardown) and restores the previous global log writer on exit — both required for safe in-process restarts. Writes `~/.ssetunnel/ssetunnel-agent.json` (PID, server URL, target, agent ID, tuning params, started_at timestamp) on start; `defer removeAgentStatusFile` cleans up on exit, guarded by PID match so a replacement's file is not removed. `ReadExternalAgentStatus()` reads the file, verifies the PID is alive, and rejects entries older than 7 days to mitigate PID-reuse false positives.
- `RunConnect(ctx, ConnectConfig) error` — stdio (`--local -`) or TCP listener mode.
- `StartNamedConnect(ctx, name, ConnectConfig) (*ConnectInstance, error)` — binds the listener synchronously (failures leave no partial entry), rejects duplicate names and stdio mode. The session goroutine owns cleanup: `defer ln.Close()` + `defer cancel()` cover every exit path, including the eager token-probe failure where `ServeListener` never installs its ctx watcher. `ConnectInstance.Config` is exported for UI listing; `Close()` returns the session's terminal run error.
- `ClampBatchSize` / `ClampConcurrency` / `ClampAgentFlags` — 1 KiB..1 MiB, 1..4. Pure functions; edge cases pinned in `session_test.go` / `login_test.go`.

## Rules

* Every outbound HTTP call uses a bounded client and request context; `http.DefaultClient` is forbidden here (a hung console would pin UI handler goroutines forever).
* Never return passwords or tokens in results/errors; error strings must be safe to log (status only for remote failures).
* Session-file access goes through `internal/auth`; do not read/write the file directly.