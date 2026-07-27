# SSE Tunnel — Reverse TCP Tunnel over HTTP

ssetunnel exposes private TCP services (SSH, databases, etc.) through a public server using an SSE-downstream + batched-POST-upstream transport. It is designed for agents behind restrictive HTTP-only proxies that allow only plain outbound HTTP(S).

---

## Global System Architecture

```
User (SSH, DB client, ...)
  │  TCP connection to agent listener (:9090)
  ▼
┌─────────────────────────────────┐
│  Server (public, reachable)     │
│  • HTTP :8080 — /events, /up   │
│  • TCP  :9090 — agent listener │
│  • HTTP :8081 — admin console  │
└──────────┬──────────────────────┘
           │  yamux over HTTP (SSE-down + POST-up)
           ▼
┌─────────────────────────────────┐
│  Agent (restricted network)     │
│  • Dials out to server HTTP     │
│  • Accepts yamux streams        │
│  • Proxies to local TCP target  │
└──────────┬──────────────────────┘
           │  TCP (e.g. 127.0.0.1:22)
           ▼
       Target Service (sshd, DB, ...)
```

**Data flow for an SSH ProxyCommand session:**
1. SSH client spawns `connect --local -` as ProxyCommand (stdin/stdout pipes)
2. Connect client dials the server via HTTP transport (GET /connect SSE + POST /connect-up)
3. Server opens a yamux stream to the agent's session
4. Agent accepts the stream and dials the local TCP target (sshd)
5. Bidirectional `io.Copy` proxy chains: SSH client ↔ connect ↔ server ↔ agent ↔ sshd

---

## Sub-Module Architecture & Directory Guides

### 1. [CLI Entry Point](cmd/ssetunnel/AGENTS.md)
* **Responsibility**: Multi-command CLI (`server`, `agent`, `connect`, `probe`) with flag parsing and lifecycle management.
* **Path**: `cmd/ssetunnel/`

### 2. [Server](internal/server/AGENTS.md)
* **Responsibility**: Public tunnel server — HTTP endpoints (`/events`, `/up`, `/probe`, `/connect`, `/connect-up`), session registry, yamux attachment, auth middleware, agent routing by `agent_id`, and bidirectional proxy between agent connections and yamux streams.
* **Path**: `internal/server/`

### 3. [Agent](internal/agent/AGENTS.md)
* **Responsibility**: Restricted-network agent — dials out to server, accepts yamux streams, proxies to local TCP target (static or dynamic from stream header), auto-reconnects with exponential backoff. Identifies itself with `agent_id` for routing.
* **Path**: `internal/agent/`

### 4. [Connect Client](internal/connect/AGENTS.md)
* **Responsibility**: User-side connection wrapper — `ServeRW` for stdio (SSH ProxyCommand) and `ServeListener` for local TCP port forwarding. Dials the server via HTTP transport (`transport.DialConnect`) with agent routing and optional dynamic target, proxies bidirectionally.
* **Path**: `internal/connect/`

### 5. [Transport](internal/transport/AGENTS.md)
* **Responsibility**: Core SSE-down + batched-POST-up transport layer — `Conn` (agent-side net.Conn), `Batcher` (write coalescing), `Pipe` (bounded in-memory pipe with deadlines), `ReorderWindow` (concurrent POST reassembly), SSE codec, capability negotiation.
* **Path**: `internal/transport/`

### 6. [Multiplexer](internal/mux/AGENTS.md)
* **Responsibility**: Thin yamux wrapper with tuned config (1 MiB stream window, 30 s keepalive, 256 accept backlog).
* **Path**: `internal/mux/`

### 7. [Auth](internal/auth/AGENTS.md)
* **Responsibility**: PostgreSQL-backed token and PIN management — bearer tokens, single-use PINs with redemption, admin sessions, user sessions with revocation, TOTP verification, agent config management (allowed targets), user permissions, read-through token cache.
* **Path**: `internal/auth/`

### 8. [Console API](internal/consoleapi/AGENTS.md)
* **Responsibility**: JSON management API (`/api/v1/...`) — TOTP login/logout, token CRUD, PIN enrollment with QR code, live session listing, agent config CRUD, user management.
* **Path**: `internal/consoleapi/`

### 9. [Console Server](internal/consoleserver/AGENTS.md)
* **Responsibility**: Combines the console API router with the React SPA static file server via `litespaserver`.
* **Path**: `internal/consoleserver/`

### 10. [Probe](internal/probe/AGENTS.md)
* **Responsibility**: Network diagnostics — measures POST body-size cliff, RTT, and per-connection vs aggregate throttling; recommends `--batch-size` and `--concurrency`.
* **Path**: `internal/probe/`

### 11. [Migrations](migrations/AGENTS.md)
* **Responsibility**: Embedded Atlas SQL migration files via `//go:embed`, exported as `FS embed.FS`.
* **Path**: `migrations/`

### 12. [Frontend](frontend/AGENTS.md)
* **Responsibility**: React admin console SPA (Vite + TypeScript + MUI v9 + orca-ui), embedded via `go:embed` for the `litespaserver`. Mercury Console light theme design system.
* **Path**: `frontend/`

---

## Core Development & Operation Rules

### Build and Test
```bash
go build ./...                                    # compile check
go vet ./...                                      # static analysis
go test ./internal/connect/... -v -timeout 30s    # focused test
go test ./... -timeout 120s                       # full suite (needs Docker for TestContainers)
```

### Local Development
```bash
./local.sh server --disable-auth                  # server without auth (dev mode)
./local.sh agent --target 127.0.0.1:22            # agent proxying to local sshd
ssh -o ProxyCommand="./local.sh connect --local -" user@127.0.0.1   # SSH through tunnel
```

### Key Conventions
* **Test-first for bugs**: Always write a reproduction test before fixing. The `TestServeRW_ServerClosesReturns` test was written to prove the SSH ProxyCommand deadlock before the fix.
* **Bidirectional proxy pattern**: Every hop uses two goroutines with `io.CopyBuffer` + `sync.Pool` 32 KiB buffers. When one direction hits EOF, the corresponding connection/stream must be closed to unblock the other direction.
* **Fail-fast model**: POST failures, stream deaths, and pipe closures are terminal — sessions die and agents reconnect. No partial recovery.
* **No goroutine leaks**: Every goroutine hangs off a context owned by its conn/session. `Close()` always tears down cleanly.

---

## Known Pitfalls & Lessons Learned

### SSH ProxyCommand Deadlock (ServeRW)
**Problem**: When the remote target (e.g., sshd) closes after sending data (keyboard-interactive prompt), `ServeRW` waited for both `io.Copy` goroutines. The r→server copy blocked on `r.Read()` from stdin (os.Pipe), which the SSH client keeps open waiting for the prompt. Deadlock: SSH waits for data from proxy, proxy waits for stdin EOF from SSH.

**Fix**: Run server→w in the main goroutine and return immediately when the server side reaches EOF. The r→server goroutine becomes fire-and-forget; process exit (in ProxyCommand mode) handles cleanup. Closing `serverConn` does NOT unblock a blocking pipe read — the read is on a different FD.

**Key insight**: TCP half-close (`CloseWrite`) signals EOF to the remote side without killing the read path. But when the *other* side closes first, you must return from the proxy function to tear down the pipe, not wait for the blocked reader.

### yamux Stream Close Semantics
yamux `stream.Close()` kills both directions — there is no half-close. This means you cannot signal "done writing" while still reading. The connect client uses TCP `CloseWrite()` on the agent connection instead.

### Agent Routing and Dynamic Target
**Agent handshake protocol**: `TOKEN [agent_id [target]]\n` (space-separated, optional fields).

When `agent_id` is provided, the server routes to that specific agent via `findYamuxByAgentID`. When `target` is also provided, the agent reads it from the yamux stream header and connects to that address (dynamic target mode).

**Agent configs** in the `agents` table define allowed targets per agent. A NULL `agent_id` row serves as the default config for all agents. The `TargetAllowed` function matches patterns like `*`, `host:*`, or `host:port`.

### SSH ProxyCommand Error Messages
**Problem**: When handshake fails, SSH only shows "Connection closed by UNKNOWN port 65535".

**Fix**: Write the error to both `os.Stderr` and `w` (stdout pipe) in `ServeRW` so SSH displays the actual error message before its generic message.

### pgx Array Scanning
**Problem**: The store uses pgx/v5 but `pq.Array()` from lib/pq is incompatible with pgx's binary protocol.

**Fix**: Scan `[]string` directly (pgx handles `text[]` natively) and use `time.Time` for timestamps (pgx returns binary timestamps, not text).

### Agent Reconnect Backoff
Sessions that survived past the 10 s health threshold reset the backoff to 0 — a drop after long uptime is a network event, not a flapping server, so retry immediately.
