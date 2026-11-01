# Server

The public side of the tunnel. Accepts agent HTTP connections (SSE downstream, POST upstream), connect client HTTP connections (SSE downstream, POST upstream), cloud shell connections, and admin console requests.

## Architecture

```
HTTP :8080                                  HTTP :8081
  │                                           │
Handler (mux.ServeMux)                   ConsoleHandler (gorilla/mux)
  │                                           │
├── /events      (SSE, agent)            ├── /console/api/v1/shell/* (cloud shell proxy)
├── /up          (POST, agent)           ├── /console/api/v1/* (consoleapi)
├── /connect     (SSE, connect client)   └── /console/* (SPA)
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
Top-level server. Owns the `Registry`, `Handler`, and optional `auth.Store` and `metrics.MetricsCollector`. Created via `NewServer(heartbeat)` or `NewServerWithBase(heartbeat, basePath)`. `SetAuthStore` and `SetMetricsCollector` recreate the handler to inject dependencies. `FindSession(agentID)` looks up a session for auto-tuner tune frame delivery.

### `Handler`
HTTP handler serving tunnel endpoints:
- **`/events?id=<sessionID>`**: Opens an SSE stream for the agent. Creates a `Session`, registers it, attaches yamux, and streams downstream bytes as base64-encoded SSE frames with heartbeat keepalives. Polls for `event: tune` frames between reads (capped at 1 s interval) and injects them as JSON control frames. Records session start/end metrics.
- **`/up`**: Accepts upstream POST batches with `X-SSET-Session` and `X-SSET-Seq` headers. Validates session, negotiates gzip, pushes to session's reorder window or serial pipe. Records POST metrics (size, RTT) on success, error metrics on rejection.
- **`/connect?id=<sessionID>&agent=<agentID>&target=<target>`**: Opens an SSE stream for connect clients. Short-polls for agent availability (3 s timeout, 25 ms poll). Finds the target agent's yamux session, opens a stream, bridges bidirectionally. The upstream goroutine is a plain byte pipe from POST data into the yamux stream (no resize multiplexing; PTY resize is shell-only and travels the control stream). Agent session death is detected promptly via `sess.Done()` monitor goroutine.
- **`/connect-up`**: Accepts upstream POST batches from connect clients, writes to the connect session's up pipe. Rejects `X-SSET-Flags` (no reorder window on connect path). Records connect upstream metrics.
- **`handleConnectResize`** (not registered in handler mux): Accepts PTY resize POST requests (`{id, cols, rows}`), sends to the connect session's resize channel. Mounted externally by `consoleserver` at `/console/api/v1/shell/resize`.
- **`/probe`**: Diagnostic endpoint — reads and discards body, returns 200.

Server capabilities advertised: `concurrency=4;batch=1048576;gzip`.

### `Session`
One tunnel session: a `net.Conn` whose `Read` yields upstream POST bytes and `Write` feeds the downstream SSE stream. Backed by two `transport.Pipe` instances (up/down, 256 KiB each).

- **Serial path** (`push`): Monotonic seq, dedup old seqs, 409 on gap.
- **Windowed path** (`pushWindowed`): `ReorderWindow` buffers out-of-order batches.
- **Tune channel**: `tuneCh` carries `metrics.TransportParams` from the auto-tuner; non-blocking `SendTune` drops if a previous tune is pending.
- **User attribution**: `userID` (atomic int64) associates the session with the user who authenticated the agent connection, enabling user-scoped visibility in the console.
- **Done channel**: `closeCh` is closed on `Close()`; monitor goroutines use it to detect session death.

### `Registry`
Thread-safe `map[string]*Session`. `Replace` closes stale sessions on reconnect. `Range` iterates under a copy. `CloseAll` tears down all sessions on shutdown.

### Cloud Shell Handlers
- **`ShellConnectHandler()`**: Serves the cloud shell SSE downstream with its own persistent-session flow (`shellCreate` opens a yamux stream and spawns a `ShellSession`; `shellReattach` re-attaches to a live one). It never routes through `handleConnect`, and `__shell__` is forced by `shellCreate` writing the target header itself. User-scoped: non-admin users can only shell into their own agents.
- **`ShellConnectUpHandler()`**: Delegates to `handleConnectUp`.
- **`ShellConnectResizeHandler()`**: Delegates to `handleConnectResize`.
- Shell SSE responses carry `X-SSET-Heartbeat-Ms` so the frontend can size its inactivity watchdog from the configured `--heartbeat`.
- **Client-writer failure**: `ShellSession.Attach` returns a per-attach `clientLost` channel. `outputConsumer` closes it (via `detachWriter`, writer-identity guarded) on any SSE write error/deadline; `shellCreate`/`shellReattach` return on it, ending the SSE response so the browser reconnects and replays the ring buffer. The session, yamux stream, and input path stay alive — a failed SSE writer must never leave the handler parked on an open response. A reattach with `takeover=1` (sent by the frontend after its watchdog fires) evicts a stale attachment whose half-open SSE will never deliver a FIN; without the flag `serveShellSSE` answers **409 Conflict before flushing any SSE header**, so a second live client is told it is busy instead of receiving a 200 with an empty body (which the browser read as a dropped stream and retried). A `SetWriteDeadline` failure with `http.ErrNotSupported` means the writer cannot express deadlines, not that the client died, and must not detach it.

### Remote Desktop Handlers
- **`RemoteAppConnectHandler()`**: Wraps `handleRemoteApp` with forced empty target. User-scoped: non-admin users can only access their own agents. Requires `agent` query parameter.
- **`RemoteAppConnectUpHandler()`**: Wraps `handleRemoteAppUp`. Validates session ownership, rejects `X-SSET-Flags`, wraps JSON body as typed `FrameInput` via `remoteapp.WriteFrame`, writes to connect session pipe.

**Remote desktop flow**:
1. Parse `id`, `agent` from query params; require `agent`
2. Short-poll for agent yamux session (3 s timeout)
3. TOCTOU re-verify ownership after poll resolves
4. Open yamux stream, write target header if dynamic target mode
5. Create `connectSession` with up pipe, store in `connectSessions` map
6. Bridge goroutine: pipe → yamux stream (with `bridgeDone` channel for clean shutdown); `streamMu` serializes writes from bridge and SSE loop
7. SSE loop: `remoteapp.ReadFrame` → base64 SSE (screenshots: parse+strip 8-byte timestamp prefix, forward WebP data only, send `FrameScreenshotAck` back to agent; screen info as named `screeninfo` event; input acks as named `inputack` event for frontend tooltip; log events as named `log` event)
8. Stream base64 encoding via `base64.NewEncoder` to avoid full-string allocation
9. Metrics: `RecordSessionStart/End`, downstream bytes per screenshot frame

**Connect flow**:
1. Parse `id`, `agent`, `target` from query params
2. Short-poll for agent yamux session (3 s timeout)
3. Validate target against agent config's `allowed_targets` (if auth enabled, skipped for `__shell__`)
4. Open yamux stream, write target header if dynamic target mode
5. Create `connectSession` with up pipe (resize nil), store in `connectSessions` map
6. Bridge bidirectionally: pipe→stream (goroutine, plain byte pipe), stream→SSE (main loop)
7. Monitor agent session death via `sess.Done()` goroutine

All validation happens **before** opening the stream, so errors are reported as HTTP errors.

## Middleware
- **`AgentAuthMiddleware`**: Bearer token (or query token for SSE) → `ValidateToken` → checks `PermAgent` → fallback user-session validation → `X-SSET-Token` response header on upgrade.
- **`ConnectAuthMiddleware`**: Bearer token (or query token for SSE) → `ValidateToken` → checks `PermConnect` → fallback to user session validation.
- **`AdminSessionMiddleware`**: Bearer token for console admin endpoints.
- **`UserSessionMiddleware`**: Bearer token for console user endpoints (any authenticated user).

## Rules
* **`WriteTimeout: 0`** on the HTTP server — must not kill SSE streams.
* **`IdleTimeout: 5 min`** — prevents silent TCP keepalive expiry killing yamux sessions.
* **`maxUpBody = 1 MiB + 64 KiB`**: Defensive cap above the batch ceiling so exactly-at-ceiling batches don't 413.
* Session `Close()` must NOT call `yamux.Close()` — yamux's `Close()` calls `s.conn.Close()` (the Session), causing a deadlock.
* **Validation before stream**: All auth/session/target checks must complete before opening the yamux stream.
* **Agent routing**: When `agent_id` is provided, use `findYamuxByAgentID` for exact match; otherwise first-match via `findYamux`.
* **Connect path has no reorder window**: Concurrent POSTs are not supported on `/connect-up`; `X-SSET-Flags` are rejected.
* **Shell resize over the control stream**: PTY resize for cloud-shell sessions is sent to the agent over the dedicated control stream (`resizeForwarder` → `SendControl`), never interleaved on the data stream. The ephemeral connect path carries no resize (its `connectSession.resize` is nil).
