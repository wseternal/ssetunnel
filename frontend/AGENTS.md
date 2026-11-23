# Frontend

Two React SPAs built with Vite + TypeScript + MUI v9:

- **Console** (`frontend/console`) — admin management UI, Mercury Console light theme, served by `internal/consoleserver` via `litespaserver`.
- **Client** (`frontend/client`) — end-user tunnel UI (login, agent, connect), served by `internal/clientserver` (`cli ui`) and the Clix desktop app.

Both are embedded into Go binaries via `go:embed`, in separate packages so server binaries never carry client assets (ADR-005).

## Structure

```
frontend/
├── console/
│   ├── dist/           # Built assets (committed for go:embed)
│   │   ├── index.html
│   │   └── assets/     # JS/CSS bundles (standard Vite output)
│   ├── src/
│   │   ├── App.tsx          # Main application component
│   │   ├── ModulesPanel.tsx # Adapter wiring the shared module page to the console API
│   │   ├── main.tsx         # Entry point
│   │   └── theme/
│   │       └── theme.ts     # Mercury Console design tokens
│   ├── index.html
│   ├── package.json
│   └── vite.config.ts
├── client/
│   ├── dist/           # Built assets (committed for go:embed)
│   ├── src/
│   │   ├── App.tsx          # Tabs + login dialog + bridge selection
│   │   ├── main.tsx         # Entry point
│   │   ├── AgentPage.tsx    # Agent config + lifecycle
│   │   ├── ConnectPage.tsx  # Named connect instances
│   │   ├── ModulesPage.tsx  # Adapter wiring the shared module page to the local proxy
│   │   ├── LoginForm.tsx    # Server dropdowns + TOTP check
│   │   ├── ServerURLField.tsx # Saved-session URL autocomplete
│   │   ├── bridge.ts        # HTTP bridge (launch-secret auth)
│   │   ├── wails.ts         # Wails adapter (null until bindings exist)
│   │   └── types.ts         # Transport-neutral Bridge contract + DTOs
│   ├── assets.go       # go:embed ClientWebRootFs (package client)
│   ├── index.html
│   ├── package.json
│   └── vite.config.ts
├── shared/
│   └── modules/         # Module UI shared verbatim by both SPAs
│       ├── api.ts       # Module surface contract: ModuleFetch, methods, summaries
│       ├── ModulesPage.tsx    # Registry + detail + OpenAPI viewer
│       └── ModuleWorkbench.tsx # OpenAPI-driven operation form, send, response
└── console.go          # go:embed ConsoleWebRootFs (package frontend)
```

## Dependencies

- **MUI v9** (`@mui/material`, `@mui/icons-material`): Material UI components
- **`frontend/shared/modules`**: the only code shared by both SPAs. It imports React and MUI **only** — `@doublefin/orca-ui` is a console dependency, so a shared component that reached for it would break the client build.
- **orca-ui** (`@doublefin/orca-ui`): Mercury Console UI kit (AdminTable, PageHeader, StatusPill, EmptyState)
- **react-router**: Routing (peer dependency)
- **xterm.js** (`@xterm/xterm`, `@xterm/addon-fit`): Terminal emulator for Cloud Shell
- **recharts** (`recharts`): Line charts for metrics visualization (throughput, latency)
- **qrcode.react** (`qrcode.react`): QR code rendering for TOTP setup

## Build

```bash
task fe:install && task fe:build     # from the repo root (bun, not npm)
```

Output goes to `frontend/console/dist/`, which is embedded by `frontend.go`.
`dist/` is a committed build artifact, so rebuild it whenever `src/` changes —
`task ci` now rebuilds it and fails on any drift against the committed bundle.

Both vite configs alias `react`, `react-dom`, `@mui/material`, and `@mui/icons-material` to the project's own `node_modules`. `../shared` sits outside each project root, so Node resolution from there cannot find those packages; symlinking the directory instead would let Rollup resolve a second React copy and break hooks. Add any new shared import to that alias list.

`fe:build` also runs the esbuild enum-miscompile guard (see RCA-001): it
self-tests its own regex, then fails closed if `dist/assets/` holds no JS or
the bundle contains a miscompiled function-local enum.

### Base Path

The frontend base path is `/console`, defined as a single constant in
`vite.config.ts`. This is the only place the base is set — the Taskfile build
tasks (`fe:build`, `fe:ci-build`) run `bun run build` with no environment
overrides. The server strips the prefix via `--console-prefix` (default
`/console`); behind a reverse proxy that already strips, pass
`--console-prefix=""`.

## Client SPA

The client UI (`frontend/client`) is a three-tab app (Agent, Connect, Modules) with a login dialog. Pages contain no transport logic: they call the `Bridge` interface (`types.ts`), implemented by `httpBridge` (browser, `cli ui`) and — once Wails bindings exist — `wailsBridge` (desktop).

- **Auth**: the launch secret arrives in the URL fragment, moves to `sessionStorage`, and is sent as `Authorization: Bearer` on every `/api` request. A fresh fragment always wins over stored state. Fetches carry a 10 s abort timeout (30 s for logout, which tears down runtimes server-side).
- **State**: both tab panels stay mounted (hidden) so config survives tab switches; AgentPage seeds its form from a running agent's status once. Status polls every 5 s; runtime errors (`error` field on the DTOs) render as alerts.
- **Build**: `task client:install && task client:build`; CI runs `client:ci-build` + `client:dist-drift`. `dist/` is committed — rebuild and commit it whenever `src/` changes. `build.target` is `es2022` (same enum-miscompile constraint as the console, see docs/rca/001).
- **Base path**: `/` (the client server serves at root, unlike the console's `/console`).
- **No frontend tests** (project policy): validate with build + typecheck; browser e2e only with explicit permission.

## Tabs

### Admin (7 tabs)
- **Sessions**: Live tunnel sessions with stats (bytes up/down, remote addr)
- **Users**: User management — CRUD, role/permissions editing, enable/disable toggle
- **Agents**: Agent config management — CRUD for allowed targets per agent
- **Statistics**: Global metrics overview (active agents, throughput, error rate), per-agent metrics cards, time-series charts (throughput + latency via recharts LineChart), tuning decision history
- **Shell**: Cloud Shell — xterm.js terminal connecting to a selected agent's PTY via SSE-down + POST-up proxy
- **Desktop**: Remote desktop viewer for an agent's screen
- **Modules**: Module registry, metadata, OpenAPI viewer, and API workbench (shared with `cli ui`)

### Non-Admin (6 tabs)
- **Sessions**: User's own tunnel sessions (scoped by user ID)
- **Agents**: Read-only agent config list
- **Statistics**: User-scoped metrics (same layout, limited data)
- **Shell**: Cloud Shell (same as admin)
- **Desktop**: same as admin
- **Modules**: same shared registry and workbench (user-scoped agents)

## Key Features

### TOTP Login Flow
1. User enters username → blur triggers `POST /user-login-check` to detect if TOTP is required
2. TOTP field appears conditionally (or recovery code input)
3. Login via `POST /user-login` with username + password + optional TOTP code
4. Recovery codes accepted as alternative to TOTP

### TOTP Self-Enrollment
- Security icon in header opens TOTP setup dialog
- Three-step flow: setup → verify (scan QR code, enter 6-digit code) → done (display + copy recovery codes)
- Uses `qrcode.react` for QR rendering

### Cloud Shell
- Agent selector populated from `/connected-agents` endpoint
- xterm.js terminal with FitAddon for responsive sizing
- SSE stream via `fetch` ReadableStream (not EventSource) for header control
- Input sent via `POST /shell/connect-up` with `X-SSET-Session` header
- Resize forwarded via `POST /shell/resize` with JSON `{id, cols, rows}`
- Binary-safe output: base64 decode → Uint8Array → `term.write(bytes)` to preserve ANSI/UTF-8

#### Failure Handling
- Server emits `event: closed` on backing-session death (agent disconnect, stream error, explicit kill); the frontend auto-reconnects with exponential backoff (1 s → 30 s cap, max 10 attempts) instead of freezing
- **SSE inactivity watchdog**: every successful stream read (including heartbeat comments) refreshes a last-byte timestamp; after `max(60 s, 4 × X-SSET-Heartbeat-Ms)` of silence the connection is aborted into the normal reconnect + ring-replay path and the next connect sends `takeover=1` so the server evicts the stale half-open attachment. This catches laptop-sleep / NAT-proxy state drops where neither data nor an error is ever delivered
- Server also ends the SSE response when its writer fails, so clients that can see the close reconnect immediately instead of waiting for the watchdog
- Input POSTs have a 10 s hard timeout (`AbortController`) that aborts the connection on hang and triggers auto-reconnect
- Resize POST failures are logged via `console.warn` for diagnostics
- Ctrl-D / `exit` / `logout` interception removed — raw PTY bytes belong to the remote shell (Ctrl-D is vim's half-page-down; `exit` typed inside psql or nested SSH is legitimate input)
- Reconnect backoff counter resets only after a stream has been continuously up for one heartbeat interval (`stableAfterMs`, measured from stream start) — resetting on every received chunk, heartbeats included, pinned backoff at its first ~1 s delay so a flapping session never backed off. Explicit user disconnect clears the pending reconnect timer and agent ref before abort so the scheduled callback bails out
- A 409 on connect means the session already has a writer. The client retries a bounded number of times (3 × 20 s) because that writer is usually its own previous connection after a page refresh, which the server only reaps when the next heartbeat write fails; it then stops with an "open in another tab" message rather than polling forever

### Metrics Visualization
- Overview cards: active agents, upload/download throughput, error rate
- Per-agent expandable cards showing current params (batch, concurrency, compress) and snapshot metrics
- Time-series LineCharts for selected agent (throughput + latency)
- Tuning decision log with parameter change details

## Design System

Follows the Mercury Console light theme:
- Canvas background: `#f8fafc`
- Paper surfaces: `#ffffff`
- Ink text: `#0f172a`
- Indigo accent: `#4f46e5`
- Hairline borders: `#e2e8f0`

Uses `AdminTable` for data tables, `StatusPill` for status badges, `PageHeader` for section headers, `EmptyState` for empty lists.

## Session Persistence

Login state is persisted to `localStorage` (sessionToken + userRole) and validated via `/api/v1/me` on page refresh. 401 responses trigger automatic logout.

## Integration

The SPA is served by `consoleserver.NewConsoleHandler` at `/` on the console listen address (`:8081`). Hashed assets under `/assets/**` are served by a dedicated handler in `consoleserver.go` that reads them from the embedded FS and marks them `immutable` — it deliberately bypasses `litespaserver`, which would force `no-store` on them, while the security headers that library applies are declared once for the whole router by the `consoleHeaders` wrapper. API routes under `/api/v1/` are handled by `consoleapi`. Shell routes under `/api/v1/shell/` are registered by `consoleserver` and proxy to the server's cloud shell handlers. Unmatched `/api/` and bare `/assets` paths hit explicit not-found guards registered before the SPA catch-all, so a wrong prefix surfaces as a 404 rather than as `index.html` with a 200. Route order and per-class headers are pinned by `internal/consoleserver/console_routes_test.go`.

## Module API Workbench

The Modules tab renders `shared/modules/ModulesPage.tsx` + `ModuleWorkbench.tsx` for every registered module — no module-specific code, and no second implementation in either SPA. It loads its form model from `GET /api/v1/modules/<agent-id>/<module-id>/operations`, which `consoleserver` derives on demand from the module's stored `openapi.yaml` via `agentmod.BuildOperationModels` (parameters, request-body schema with local `$ref`s flattened, recursion truncated). Requests are sent through a `ModuleFetch` the surface injects: the console fetches `<base>/api/v1/modules/...` with the session bearer token, and `cli ui` fetches its own authenticated local proxy (`/api/v1/modules/...`), which forwards to the loopback module API the agent published. Both call `POST /api/v1/modules/invoke/<agent-id>/<module-id>/<path>`. A method the browser refuses to send (TRACE) travels as `POST` plus `X-SSET-Method-Override: TRACE`, restored by `internal/moduleapi` before the module sees it, and the form says so while that operation is selected.

Contract for the component: operations are ordered by path then method; path parameters are always required; query values left blank are omitted; a JSON content type enables client-side JSON validation before sending; responses are streamed and read up to 200 KiB, then cancelled and shown with a truncation notice, so an oversized artifact never lands in tab memory whole. Non-JSON bodies open empty with only a `Content-Type` header, and every module switch aborts the previous operation-list fetch. The raw OpenAPI YAML is loaded on demand from `GET /api/v1/modules/openapi/<agent-id>/<module-id>`. The catalog epoch (`X-Catalog-Epoch`) is shown when the surface forwards it.

cURL snippets are surface-specific on purpose: the console composes `<origin><base>/api/v1/modules/invoke/...` with a `Bearer $SSETUNNEL_TOKEN` placeholder, while `cli ui` composes the agent's own `module_api_url` (no secret) because that endpoint is callable by any local script. `cli ui` hides the registry entirely when the running agent published no module endpoint.

## Rules
* Always rebuild `dist/` before committing changes that touch `src/` — CI enforces it with `task fe:build` + `git diff --exit-code -- frontend/console/dist`.
* `/assets/**` is served by the dedicated handler, not by `litespaserver.Config.StaticPaths`; anything registered after the SPA catch-all silently returns `index.html`.
* Use theme tokens instead of raw hex colors.
* Use orca-ui kit components (AdminTable, PageHeader, StatusPill, EmptyState) for consistency — except inside `frontend/shared/modules`, which must stay on plain MUI so the client SPA can build it.
* Both bundles are committed: rebuild `frontend/console/dist` **and** `frontend/client/dist` whenever shared or surface sources change.
* Shell SSE uses `fetch` ReadableStream (not `EventSource`) to control auth headers.
* Binary terminal output must go through `Uint8Array` to avoid UTF-8 corruption of ANSI escape sequences.
