# Console Server

Combines the console API router with the React SPA static file server and cloud shell proxy endpoints.

## Architecture

Uses `gorilla/mux` to mount, **in this registration order** (mux matches the first
route that fits, so the order is load-bearing and pinned by `console_routes_test.go`):
- `/api/v1/shell/connect` (GET) → tunnel handler's `ShellConnectHandler` (cloud shell SSE)
- `/api/v1/shell/connect-up` (POST) → tunnel handler's `ShellConnectUpHandler`
- `/api/v1/shell/resize` (POST) → tunnel handler's `ShellConnectResizeHandler`
- `/api/v1/*` (prefix) → `consoleapi.NewRouter` (JSON management API)
- `/assets/*` (GET, HEAD) → dedicated embedded-FS handler for content-hashed Vite output
- `/api/` (prefix), exact `/api` and exact `/assets` (GET, HEAD) → not-found guards
- `/` (catch-all GET, HEAD) → `litespaserver.ServeRoot` (React SPA) — must stay last

`OPTIONS *` never reaches the router either: `net/http` answers it with its own
`globalOptionsHandler`, so the console `http.Server` sets
`DisableGeneralOptionsHandler: true`. The tunnel server on `:8080` has no header
wrapper — this contract covers the console port only.

The SPA and its assets are served from `frontend.ConsoleWebRootFs` (embedded via
`go:embed`). That FS is `fs.Sub(embed.FS, "console/dist")`, and `fs.Sub`'s `Open`
gates on `fs.ValidPath`, which rejects a leading slash — so `r.URL.Path` must be
trimmed before it is used as an FS name. `http.ServeFileFS` does this for you.

Assets bypass `litespaserver.setBaseHeaders` on purpose (`no-store` there would
defeat content-hashed filenames), so the security headers are declared once for
the whole router by `consoleHeaders`, which wraps the handler
`NewConsoleHandler` returns. It mutates the header map and then calls the inner
handler with the **original** `http.ResponseWriter` — wrapping the writer in a
struct breaks `server.shellCreate`'s direct `w.(http.Flusher)` assertion (the
cloud shell answers 500 `streaming unsupported`) and, if `SetWriteDeadline`
stops being reachable, silently disables SSE dead-client detection, because
`http.ErrNotSupported` means "cannot express a deadline", not "client died".
`mux`'s `r.Use` is not an alternative: it runs only for matched routes, so 404,
405 and path-cleanup 301 responses would ship with no headers at all.

Handlers overwrite the defaults with `Set`. The module invocation proxy is the
one exception: it copies a module's own `Cache-Control`/`ETag` with `Add`, so it
deletes each key before re-adding it — otherwise the response carries two values
and a cache reading the last one can store user-scoped module output.

`Cache-Control` follows the same rule: the wrapper declares `no-store` before
dispatch (nothing has claimed it yet at that point), so user-scoped API JSON,
404s, 405s and 301s stay uncacheable while a known-type hashed asset overrides it
with `immutable`. An asset whose MIME type had to be defaulted to
`application/octet-stream` gets `private, max-age=0, must-revalidate` instead —
`nosniff` means the browser rejects it, and `immutable` would pin that broken
answer through the fix.

Misses log through `logConsoleMiss`, one record a minute across all miss sites in
this package: an unauthenticated `/assets/` request otherwise writes a journal
line per call, and a limiter that records the attempt as well as the log goes
permanently silent under sub-minute probing.

## Constructor

```go
func NewConsoleHandler(ctx context.Context, store *auth.Store, reg *server.Registry, mc *metrics.MetricsCollector, srv *server.Server) http.Handler
```

`mc` may be nil when metrics are disabled. `srv` provides the tunnel handler for cloud shell proxying; when it is nil the shell, remote-desktop and module routes are not registered. `store` may be nil (see the `--disable-auth` rule below). There is deliberately no database parameter: with `EmbeddedContent` set `litespaserver` locks to the embedded version and never queries, so `NewServer` is called with a nil pool. `CDNVersion: "embedded"` is what makes that safe: without it `resolveEmbedded` rejects a `console/dist` with no `index.html` and `NewManager` falls through to the DB provider, which panics on a nil pool inside `NewConsoleHandler` and takes the whole server down at startup.

## Cloud Shell Proxy

Shell endpoints are registered **before** the `/api/v1/` prefix catch-all (gorilla/mux matches the first route). They use `UserSessionMiddleware` for auth. The `ShellConnectHandler` forces `target=__shell__` and validates user-scoped agent access (non-admin users can only shell into their own agents).

## Rules
* The console server runs on a separate port (`:8081` by default) from the tunnel HTTP server (`:8080`).
* It starts whenever `--console-listen` is non-empty — including under `--disable-auth`, where `pool` and `store` are nil and `UserSessionMiddleware` injects a synthetic admin session (see `internal/server/middleware.go`). Pass `--console-listen=""` to keep it off.
* Shell routes MUST be registered before the API prefix catch-all.
* CSP is disabled for the SPA (`litespaserver.CSPConfig{Disable: true}`).
* Anything registered after the SPA catch-all silently serves `index.html`.
