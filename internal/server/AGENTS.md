# Server

The public side of the tunnel. Accepts agent HTTP connections (SSE downstream, POST upstream), user TCP connections on the entry listener, and admin console requests.

## Architecture

```
HTTP :8080                TCP :9090              HTTP :8081
  │                         │                      │
Handler (mux.ServeMux)  ServeEntry()          ConsoleHandler
  │                         │                      │
├── /events (SSE)       proxyEntry()          ├── /api/v1/* (consoleapi)
├── /up   (POST)            │                  └── /* (SPA)
└── /probe                  │
  │                         │
Registry ←──── Session ←───┘
  │
yamux.Server(session)
  │
OpenStream() → bidirectional io.Copy ↔ entry conn
```

## Core Types

### `Server`
Top-level server. Owns the `Registry`, `Handler`, and optional `auth.Store`. Created via `NewServer(heartbeat)`.

### `Handler`
HTTP handler serving tunnel endpoints:
- **`/events?id=<sessionID>`**: Opens an SSE stream for the agent. Creates a `Session`, registers it, attaches yamux, and streams downstream bytes as base64-encoded SSE frames with heartbeat keepalives.
- **`/up`**: Accepts upstream POST batches with `X-SSET-Session` and `X-SSET-Seq` headers. Validates session, negotiates gzip, pushes to session's reorder window or serial pipe.
- **`/probe`**: Diagnostic endpoint — reads and discards body, returns 200.

Server capabilities advertised: `concurrency=4;batch=1048576;gzip`.

### `Session`
One tunnel session: a `net.Conn` whose `Read` yields upstream POST bytes and `Write` feeds the downstream SSE stream. Backed by two `transport.Pipe` instances (up/down, 256 KiB each).

- **Serial path** (`push`): Monotonic seq, dedup old seqs, 409 on gap.
- **Windowed path** (`pushWindowed`): `ReorderWindow` buffers out-of-order batches.

### `Registry`
Thread-safe `map[string]*Session`. `Replace` closes stale sessions on reconnect. `Range` iterates under a copy.

### `proxyEntry`
Opens a yamux stream via `findYamux().OpenStream()`, then runs two goroutines for bidirectional `io.CopyBuffer`. Performs token handshake with optional agent routing:

**Handshake protocol**: `TOKEN [agent_id [target]]\n`

1. Read handshake line, parse token/agent_id/target
2. Validate token via `ValidateToken`
3. Look up session by `agent_id` (if provided) or first-match
4. Validate target against agent config's `allowed_targets`
5. Send `OK\n` or `ERR <message>\n`
6. Open yamux stream with target header (if dynamic target mode)

All validation happens **before** sending OK, so errors are reported during handshake rather than corrupting the data stream.

## Middleware
- **`AgentAuthMiddleware`**: Bearer token → `ValidateToken` → fallback PIN redemption → `X-SSET-Token` response header on upgrade.
- **`AdminSessionMiddleware`**: Bearer token or session cookie for console admin endpoints.

## Rules
* **`WriteTimeout: 0`** on the HTTP server — must not kill SSE streams.
* **`maxUpBody = 1 MiB + 64 KiB`**: Defensive cap above the batch ceiling so exactly-at-ceiling batches don't 413.
* Session `Close()` must NOT call `yamux.Close()` — yamux's `Close()` calls `s.conn.Close()` (the Session), causing a deadlock.
* **Validation before OK**: All auth/session/target checks must complete before sending `OK\n` to prevent error messages from corrupting the data stream.
* **Agent routing**: When `agent_id` is provided, use `findYamuxByAgentID` for exact match; otherwise first-match via `findYamux`.
