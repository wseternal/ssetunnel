# ADR-005: Client Surfaces — Product Split

## Status

Accepted. **Implemented for the CLI and browser surfaces.** The Clix desktop
module is committed as a skeleton: its service methods are stubs and the
Wails bindings are not generated yet (see Remaining Work).

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

- `frontend/console.go` (package `frontend`) embeds only the admin console SPA (`console/dist`) and exports `ConsoleWebRootFs`.
- `frontend/client/assets.go` (package `client`) embeds only the client SPA (`client/dist`) and exports `ClientWebRootFs`.
- Server binaries import `frontend` for the admin console. They never embed client assets (verified via `go list -deps ./cmd/server`).
- The browser server (`internal/clientserver`) and Clix import `frontend/client` for the client UI.
- Known residual: `cmd/cli` transitively embeds the console bundle because `internal/clicommand` imports `internal/command`, which owns `RunServer` and imports `internal/consoleserver`. Splitting the shared flag/service helpers out of `internal/command` would remove it; deferred as structural churn.
- Clix serves `ClientWebRootFs` through Wails `AssetFileServerFS`. It starts no HTTP listener.

### Browser Security Contract

The `cli ui` command starts a secured local HTTP server:

1. Bind only to a loopback address (`127.0.0.1:0` by default). Non-loopback `--listen` values are rejected before the listener opens: the Host check is not a compensating control because a non-browser client controls its own Host header.
2. Generate a 32-byte random launch secret per invocation.
3. Put the secret in the URL fragment (`http://127.0.0.1:PORT/#SECRET`).
4. The frontend moves the fragment to `sessionStorage` and removes it from the URL. A fresh fragment always wins over stored state (relaunch in the same tab).
5. Every API request sends the secret as `Authorization: Bearer <secret>`.
6. The server validates with constant-time comparison.
7. Enforce exact Host (`127.0.0.1`, `localhost`, `::1`, parsed with `net.SplitHostPort` so bracketed IPv6 works) and same-origin Origin checks (http scheme, loopback hostname, matching port).
8. Apply CSP (`default-src 'self'`), `nosniff`, `DENY` frames, `no-referrer`, `no-store` headers to every response class, including mux-generated 404/405/301. The SPA is served by dedicated index/asset handlers (not litespaserver, which `Set()`s its own headers over any outer wrapper); hashed assets override caching with `immutable` + ETag.
9. Limit JSON bodies to 64 KiB. Apply server timeouts.
10. Return 404 for unknown API and asset paths.
11. Never return passwords or remote session tokens in DTOs, errors, or logs. Login/TOTP errors carry status codes only — the remote console's response body is attacker-influenced and must not reach logs.

Tradeoff: the launch secret is printed to the terminal and passed in the
browser-open command's argv, so it is visible in scrollback and `ps` for the
child's lifetime. On shared hosts, prefer `--no-open` and copy the URL
manually.

### Desktop (Clix)

- Clix is a separate Go module at `app/clix/` with a `wails/v3` dependency.
- It registers a `ClientService` intended to wrap `clientapp` with narrow methods.
- Wails generates bindings from `app/clix` into `app/clix/frontend/bindings/` with `GOWORK=off`.
- The frontend bridge selects the Wails adapter when `window.runtime` exists, HTTP otherwise.
- Clix calls `app.OnShutdown` to close owned agent and connect runtimes.
- `ApplicationShouldTerminateAfterLastWindowClosed: true` ensures macOS cleanup.

### Remaining Work (Clix)

The committed Clix module is a skeleton. Before it is a usable product:

1. Make the module buildable from a fresh clone: track `app/clix/build/appicon.png` and `build/config.yml` (currently gitignored by `build/`), run `go mod tidy` (the committed `go.sum` is incomplete), and add a root `clix:build` task plus CI coverage.
2. Promote the browser server's runtime manager (`internal/clientserver/runtime.go`) to an exported shared package so Clix and the browser server enforce one ownership contract.
3. Wire `ClientService` login/session stubs to `clientapp.Login` / `CheckTOTPRequired` / `ResolveSession`; add the missing `RestartAgent` / `RestartConnect` methods; use typed DTO structs instead of `map[string]any` so generated bindings keep type safety.
4. Add a `wails3 generate bindings` task and implement the `wails.ts` adapter mapping generated types to the `Bridge` DTOs (`wailsBridge` is currently `null`).
5. Wire `app.OnShutdown` to the runtime owner.

### Runtime Ownership

- Each browser or desktop instance owns at most one agent and multiple named connect runtimes.
- These run in-process. No additional tunnel hop.
- Duplicate names and bind failures leave no partial runtime entry.
- UI runtime `Close` cancels work, closes resources, and waits for goroutines (bounded by a 20 s teardown cap).
- Closing a UI does not stop separately installed OS services.
- Logout stops the owned agent and connect runtimes before deleting the session entry: a tunnel must not keep running on a deleted credential until silent server-side expiry.
- The browser server's lifecycle state lives in `internal/clientserver/runtime.go` (per-run structs, operation mutex, atomic error snapshots). It is unexported; Clix will need it promoted to a shared package when its bindings are wired.

### Service Namespace

`cli`, `ssetunnel`, and `server` share the `ssetunnel-*` OS service names, so
any of them can manage a service installed by another. The installed argv
differs by binary shape: multi-command binaries install `["<subcommand>",
"run", …flags]`; the single-command `server` binary installs `["run", …flags]`
and also accepts the legacy prefixed form for in-place binary upgrades.

## Consequences

- Operators can deploy `server` without client code.
- Users get a native desktop experience via Clix once its bindings are wired (see Remaining Work).
- Browser access via `cli ui` requires no installation.
- Legacy `ssetunnel` binary remains compatible for existing SSH `ProxyCommand` and service installations.
- CLI and browser surfaces share one implementation path (`clientapp`), preventing drift; Clix joins it when its stubs are wired.

## Rejected Alternatives

- **Copy command logic into each main package:** Rejected — behavior would drift.
- **Remove `cmd/ssetunnel` immediately:** Rejected — existing installations would break.
- **Duplicate the React source under `app/clix`:** Rejected — workflows would diverge.
- **Build a separate Clix frontend bundle:** Rejected — `AssetFileServerFS` can serve the shared embedded client filesystem.
- **Run a loopback HTTP server inside Clix:** Rejected — direct Wails bindings avoid another listener.
- **Control the CLI through subprocesses:** Rejected — Clix would depend on an external binary.
