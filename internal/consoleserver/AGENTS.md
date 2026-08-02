# Console Server

Combines the console API router with the React SPA static file server and cloud shell proxy endpoints.

## Architecture

Uses `gorilla/mux` to mount:
- `/console/api/v1/shell/connect` (GET) → tunnel handler's `ShellConnectHandler` (cloud shell SSE)
- `/console/api/v1/shell/connect-up` (POST) → tunnel handler's `ShellConnectUpHandler`
- `/console/api/v1/shell/resize` (POST) → tunnel handler's `ShellConnectResizeHandler`
- `/console/api/v1/*` (prefix) → `consoleapi.NewRouter` (JSON management API, stripped of `/console`)
- `/console` → redirect to `/console/`
- `/console/*` (catch-all GET) → `litespaserver.ServeRoot` (React SPA)

The SPA is served from `frontend.ConsoleWebRootFs` (embedded via `go:embed`).

## Constructor

```go
func NewConsoleHandler(ctx context.Context, pool *pgxpool.Pool, store *auth.Store, reg *server.Registry, mc *metrics.MetricsCollector, srv *server.Server) http.Handler
```

`mc` may be nil when metrics are disabled. `srv` provides the tunnel handler for cloud shell proxying.

## Cloud Shell Proxy

Shell endpoints are registered **before** the `/console/api/v1/` prefix catch-all (gorilla/mux matches the first route). They use `UserSessionMiddleware` for auth. The `ShellConnectHandler` forces `target=__shell__` and validates user-scoped agent access (non-admin users can only shell into their own agents).

## Rules
* The console server runs on a separate port (`:8081` by default) from the tunnel HTTP server (`:8080`).
* Only started when auth is enabled (not with `--disable-auth`).
* Shell routes MUST be registered before the API prefix catch-all.
* CSP is disabled for the SPA (`litespaserver.CSPConfig{Disable: true}`).
