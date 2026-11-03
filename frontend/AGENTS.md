# Frontend

React admin console SPA built with Vite + TypeScript + MUI v9 + orca-ui. Mercury Console light theme design system. Embedded into the Go binary via `go:embed` for the `litespaserver`.

## Structure

```
frontend/
├── console/
│   ├── dist/           # Built assets (committed for go:embed)
│   │   ├── index.html
│   │   └── assets/     # JS/CSS bundles (standard Vite output)
│   ├── src/
│   │   ├── App.tsx          # Main application component
│   │   ├── main.tsx         # Entry point
│   │   └── theme/
│   │       └── theme.ts     # Mercury Console design tokens
│   ├── index.html
│   ├── package.json
│   └── vite.config.ts
└── frontend.go         # go:embed ConsoleWebRootFs
```

## Dependencies

- **MUI v9** (`@mui/material`, `@mui/icons-material`): Material UI components
- **orca-ui** (`@doublefin/orca-ui`): Mercury Console UI kit (AdminTable, PageHeader, StatusPill, EmptyState)
- **react-router**: Routing (peer dependency)
- **xterm.js** (`@xterm/xterm`, `@xterm/addon-fit`): Terminal emulator for Cloud Shell
- **recharts** (`recharts`): Line charts for metrics visualization (throughput, latency)
- **qrcode.react** (`qrcode.react`): QR code rendering for TOTP setup

## Build

```bash
cd frontend/console && npm install && npx vite build
```

Output goes to `frontend/console/dist/`, which is embedded by `frontend.go`.

## Tabs

### Admin (5 tabs)
- **Sessions**: Live tunnel sessions with stats (bytes up/down, remote addr)
- **Users**: User management — CRUD, role/permissions editing, enable/disable toggle
- **Agents**: Agent config management — CRUD for allowed targets per agent
- **Statistics**: Global metrics overview (active agents, throughput, error rate), per-agent metrics cards, time-series charts (throughput + latency via recharts LineChart), tuning decision history
- **Shell**: Cloud Shell — xterm.js terminal connecting to a selected agent's PTY via SSE-down + POST-up proxy

### Non-Admin (4 tabs)
- **Sessions**: User's own tunnel sessions (scoped by user ID)
- **Agents**: Read-only agent config list
- **Statistics**: User-scoped metrics (same layout, limited data)
- **Shell**: Cloud Shell (same as admin)

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

Login state is persisted to `localStorage` (sessionToken + userRole) and validated via `/console/api/v1/me` on page refresh. 401 responses trigger automatic logout.

## Integration

The SPA is served by `consoleserver.NewConsoleHandler` via `litespaserver.ServeRoot` at `/console/` on the console listen address (`:8081`). The SPA handler is wrapped with `http.StripPrefix("/console", ...)` so litespaserver sees paths relative to the SPA root. Static asset paths (`/assets/**`) are allow-listed via `litespaserver.Config.StaticPaths`. API routes under `/console/api/v1/` are handled by `consoleapi`. Shell routes under `/console/api/v1/shell/` are registered by `consoleserver` and proxy to the server's cloud shell handlers.

## Rules
* Always rebuild `dist/` before committing Go changes that touch the frontend.
* Static asset paths (`/assets/**`) are allow-listed by `litespaserver.Config.StaticPaths`.
* Use theme tokens instead of raw hex colors.
* Use orca-ui kit components (AdminTable, PageHeader, StatusPill, EmptyState) for consistency.
* Shell SSE uses `fetch` ReadableStream (not `EventSource`) to control auth headers.
* Binary terminal output must go through `Uint8Array` to avoid UTF-8 corruption of ANSI escape sequences.
