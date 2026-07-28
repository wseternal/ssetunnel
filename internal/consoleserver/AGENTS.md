# Console Server

Combines the console API router with the React SPA static file server.

## Architecture

Uses `gorilla/mux` to mount:
- `/api/v1/*` → `consoleapi.NewRouter` (JSON management API)
- `/*` (catch-all GET) → `litespaserver.ServeRoot` (React SPA)

The SPA is served from `frontend.ConsoleWebRootFs` (embedded via `go:embed`).

## Constructor

```go
func NewConsoleHandler(ctx context.Context, pool *pgxpool.Pool, store *auth.Store, reg *server.Registry, totpSecret string) http.Handler
```

## Rules
* The console server runs on a separate port (`:8081` by default) from the tunnel HTTP server (`:8080`).
* Only started when auth is enabled (not with `--disable-auth`).
