# Server

The public side of the tunnel. Accepts agent HTTP connections (SSE downstream, POST upstream), connect client HTTP connections (SSE downstream, POST upstream), and admin console requests.

## Architecture

```
HTTP :8080                                  HTTP :8081
  │                                           │
Handler (mux.ServeMux)                   ConsoleHandler
  │                                           │
├── /events      (SSE, agent)            ├── /api/v1/* (consoleapi)
├── /up          (POST, agent)           └── /* (SPA)
├── /connect     (SSE, connect client)
├── /connect-up  (POST, connect client)
└── /probe
  │
Registry ←──── Session
  │
yamux.Server(session)
  │
OpenStream() → bidirectional io.Copy ↔ connect client
```

## Core Types

### `Server`
Top-level server. Owns the `Registry`, `Handler`, and optional `auth.Store`. Created via `NewServer(heartbeat)`.

### `Handler`
HTTP handler serving tunnel endpoints:
- **`/events?id=<sessionID>`**: Opens an SSE stream for the agent. Creates a `Session`, registers it, attaches yamux, and streams downstream bytes as base64-encoded SSE frames with heartbeat keepalives.
- **`/up`**: Accepts upstream POST batches with `X-SSET-Session` and `X-SSET-Seq` headers. Validates session, negotiates gzip, pushes to session's reorder window or serial pipe.
- **`/connect?id=<sessionID>&agent=<agentID>&target=<target>`**: Opens an SSE stream for connect clients. Finds the target agent's yamux session, opens a stream, bridges bidirectionally between HTTP transport and yamux.
- **`/connect-up`**: Accepts upstream POST batches from connect clients, writes to the connect session's up pipe.
- **`/probe`**: Diagnostic endpoint — reads and discards body, returns 200.

Server capabilities advertised: `concurrency=4;batch=1048576;gzip`.

### `Session`
One tunnel session: a `net.Conn` whose `Read` yields upstream POST bytes and `Write` feeds the downstream SSE stream. Backed by two `transport.Pipe` instances (up/down, 256 KiB each).

- **Serial path** (`push`): Monotonic seq, dedup old seqs, 409 on gap.
- **Windowed path** (`pushWindowed`): `ReorderWindow` buffers out-of-order batches.

### `Registry`
Thread-safe `map[string]*Session`. `Replace` closes stale sessions on reconnect. `Range` iterates under a copy.

### `handleConnect`
Opens a yamux stream via `findYamuxByAgentID` or `findYamux`, creates a `connectSession` bridge (up pipe + cancel), then runs two goroutines: one copies pipe→stream (upstream), and the main goroutine reads stream→SSE frames (downstream) with heartbeat keepalives.

**Connect flow**:
1. Parse `id`, `agent`, `target` from query params
2. Find agent yamux session by agent ID (or first-match)
3. Validate target against agent config's `allowed_targets` (if auth enabled)
4. Open yamux stream, write target header if dynamic target mode
5. Create `connectSession` with up pipe, store in `connectSessions` map
6. Bridge bidirectionally: pipe→stream (goroutine), stream→SSE (main loop)

All validation happens **before** opening the stream, so errors are reported as HTTP errors.

## Middleware
- **`AgentAuthMiddleware`**: Bearer token → `ValidateToken` → checks `PermAgent` → fallback PIN redemption → `X-SSET-Token` response header on upgrade.
- **`ConnectAuthMiddleware`**: Bearer token (or query token for SSE) → `ValidateToken` → checks `PermConnect` → fallback to user session validation.
- **`AdminSessionMiddleware`**: Bearer token or session cookie for console admin endpoints.

## Rules
* **`WriteTimeout: 0`** on the HTTP server — must not kill SSE streams.
* **`maxUpBody = 1 MiB + 64 KiB`**: Defensive cap above the batch ceiling so exactly-at-ceiling batches don't 413.
* Session `Close()` must NOT call `yamux.Close()` — yamux's `Close()` calls `s.conn.Close()` (the Session), causing a deadlock.
* **Validation before stream**: All auth/session/target checks must complete before opening the yamux stream.
* **Agent routing**: When `agent_id` is provided, use `findYamuxByAgentID` for exact match; otherwise first-match via `findYamux`.
