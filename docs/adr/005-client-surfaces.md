# ADR-005: Client Surfaces — Product Split

## Status

Accepted. **Partially Implemented** — the browser API endpoints for agent and connect lifecycle, and all Clix desktop bindings, are stubs returning 501 Not Implemented. The CLI, session, and login workflows are fully operational.

## Context

The original `ssetunnel` binary combined server, agent, connect, probe, module, and login commands into one executable. Operators deploying only the tunnel server had to ship the full binary including client workflows, and desktop users needed a separate CLI workflow instead of a native application.

The console frontend (`frontend/console`) served the admin management UI only. No client-facing UI existed beyond the terminal commands.

## Decision

Split the product surface into four artifacts sharing one implementation:

| Product | Path | Purpose |
|---------|------|---------|
| `server` | `cmd/server/` | Tunnel server only |
| `cli` | `cmd/cli/` | Client commands + `ui` for browser access |
| `ssetunnel` | `cmd/ssetunnel/` | Frozen compatibility wrapper |
| `clix` | `app/clix/` | Wails v3 native desktop app |

### Shared Workflow Packages

- `internal/command` owns flags, prompts, help text, OS service dispatch, and module management.
- `internal/clientapp` owns login, session resolution, agent lifecycle, and named connect workflows.
- All surfaces (CLI foreground, CLI service, browser API, Wails bindings) call the same `clientapp` functions.

### Asset Boundaries

- `frontend/console/` embeds only the admin console SPA (`frontend/console/assets.go`).
- `frontend/` embeds only the client SPA (`frontend/frontend.go` exports `ClientWebRootFs`).
- Server binaries import `frontend/console` for the admin console. They never embed client assets.
- CLI and Clix embed `ClientWebRootFs` for the client UI.
- Clix serves `ClientWebRootFs` through Wails `AssetFileServerFS`. It starts no HTTP listener.

### Browser Security Contract

The `cli ui` command starts a secured local HTTP server:

1. Bind only to a loopback address (`127.0.0.1:0` by default).
2. Generate a 32-byte random launch secret per invocation.
3. Put the secret in the URL fragment (`http://127.0.0.1:PORT/#SECRET`).
4. The frontend moves the fragment to `sessionStorage` and removes it from the URL.
5. Every API request sends the secret as `Authorization: Bearer <secret>`.
6. The server validates with constant-time comparison.
7. Enforce exact Host (`127.0.0.1`, `localhost`, `::1`) and same-origin Origin checks.
8. Apply CSP (`default-src 'self'`), `nosniff`, `DENY` frames, `no-referrer`, `no-store` headers.
9. Limit JSON bodies to 64 KiB. Apply server timeouts.
10. Return 404 for unknown API and asset paths.
11. Never return passwords or remote session tokens in DTOs, errors, or logs.

### Desktop (Clix)

- Clix is a separate Go module at `app/clix/` with `wails/v3` dependency.
- It registers a `ClientService` with narrow methods wrapping `clientapp`.
- Wails generates bindings from `app/clix` into `app/clix/frontend/bindings/` with `GOWORK=off`.
- The frontend bridge selects the Wails adapter when `window.runtime` exists, HTTP otherwise.
- Clix calls `app.OnShutdown` to close owned agent and connect runtimes.
- `ApplicationShouldTerminateAfterLastWindowClosed: true` ensures macOS cleanup.

### Runtime Ownership

- Each browser or desktop instance owns at most one agent and multiple named connect runtimes.
- These run in-process. No additional tunnel hop.
- Duplicate names and bind failures leave no partial runtime entry.
- UI runtime `Close` cancels work, closes resources, and waits for goroutines.
- Closing a UI does not stop separately installed OS services.

## Consequences

- Operators can deploy `server` without client code.
- Users get a native desktop experience via Clix.
- Browser access via `cli ui` requires no installation.
- Legacy `ssetunnel` binary remains compatible for existing SSH `ProxyCommand` and service installations.
- All surfaces share one implementation path, preventing drift.

## Rejected Alternatives

- **Copy command logic into each main package:** Rejected — behavior would drift.
- **Remove `cmd/ssetunnel` immediately:** Rejected — existing installations would break.
- **Duplicate the React source under `app/clix`:** Rejected — workflows would diverge.
- **Build a separate Clix frontend bundle:** Rejected — `AssetFileServerFS` can serve the shared embedded client filesystem.
- **Run a loopback HTTP server inside Clix:** Rejected — direct Wails bindings avoid another listener.
- **Control the CLI through subprocesses:** Rejected — Clix would depend on an external binary.
