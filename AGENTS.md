# SSE Tunnel — Reverse TCP Tunnel over HTTP

ssetunnel exposes private TCP services (SSH, databases, etc.) through a public server using an SSE-downstream + batched-POST-upstream transport. It is designed for agents behind restrictive HTTP-only proxies that allow only plain outbound HTTP(S).

---

## Global System Architecture

```
User (SSH, DB client, ...)
  │  TCP connection to local connect listener
  ▼
┌─────────────────────────────────────────────────────────┐
│  Server (public, reachable)                              │
│  • HTTP :8080 — /events, /up, /connect, /connect-up     │
│  • HTTP :8081 — admin console + cloud shell + metrics   │
└──────────┬──────────────────────────────────────────────┘
           │  yamux over HTTP (SSE-down + POST-up)
           ▼
┌─────────────────────────────────┐
│  Agent (restricted network)     │
│  • Dials out to server HTTP     │
│  • Accepts yamux streams        │
│  • Proxies to local TCP target  │
│  • Or spawns local shell (PTY)  │
└──────────┬──────────────────────┘
           │  TCP (e.g. 127.0.0.1:22) or PTY shell
           ▼
       Target Service (sshd, DB, ...) or local shell
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
* **Responsibility**: Public tunnel server — HTTP endpoints (`/events`, `/up`, `/probe`, `/connect`, `/connect-up`), session registry, yamux attachment, auth middleware, agent routing by `agent_id`, bidirectional proxy between agent connections and yamux streams, cloud shell proxy (`__shell__` target with PTY resize), metrics collection hooks, and auto-tune SSE frame injection.
* **Path**: `internal/server/`

### 3. [Agent](internal/agent/AGENTS.md)
* **Responsibility**: Restricted-network agent — dials out to server, accepts yamux streams, proxies to local TCP target (static or dynamic from stream header) or spawns a local shell with PTY (`__shell__` target), auto-reconnects with exponential backoff (500 ms → 30 s cap). Identifies itself with `agent_id` for routing. Supports server-driven auto-tuning of batch size and compression via SSE `event: tune` frames.
* **Path**: `internal/agent/`

### 4. [Connect Client](internal/connect/AGENTS.md)
* **Responsibility**: User-side connection wrapper — `ServeRW` for stdio (SSH ProxyCommand) and `ServeListener` for local TCP port forwarding. Dials the server via HTTP transport (`transport.DialConnect`) with agent routing and optional dynamic target, proxies bidirectionally. `ServeListener` performs an eager token validation probe at startup and retries dials with exponential backoff.
* **Path**: `internal/connect/`

### 5. [Transport](internal/transport/AGENTS.md)
* **Responsibility**: Core SSE-down + batched-POST-up transport layer — `Conn` (agent-side net.Conn), `Batcher` (write coalescing), `Pipe` (bounded in-memory pipe with deadlines), `ReorderWindow` (concurrent POST reassembly), SSE codec, capability negotiation.
* **Path**: `internal/transport/`

### 6. [Multiplexer](internal/mux/AGENTS.md)
* **Responsibility**: Thin yamux wrapper with tuned config (4 MiB stream window, 30 s keepalive, 256 accept backlog).
* **Path**: `internal/mux/`

### 7. [Auth](internal/auth/AGENTS.md)
* **Responsibility**: PostgreSQL-backed token and PIN management — bearer tokens, single-use PINs with redemption, user sessions with revocation, per-user TOTP enrollment with recovery codes (HMAC-SHA256 digests), agent config management (allowed targets), user permissions (`can_connect`, `can_create_agent`), password hashing (bcrypt), multi-server session file (`~/.ssetunnel/session`), read-through token validation.
* **Path**: `internal/auth/`

### 8. [Console API](internal/consoleapi/AGENTS.md)
* **Responsibility**: JSON management API (`/api/v1/...`) — username/password login with per-user TOTP and recovery code fallback, rate-limited login (password + TOTP), TOTP self-enrollment with QR code and recovery codes, token CRUD, live session listing (user-scoped), agent config CRUD, user management with disable toggle, connected-agents listing, metrics overview/samples/decisions endpoints.
* **Path**: `internal/consoleapi/`

### 9. [Console Server](internal/consoleserver/AGENTS.md)
* **Responsibility**: Combines the console API router with the React SPA static file server via `litespaserver`. Proxies cloud shell endpoints (`/console/api/v1/shell/connect`, `/connect-up`, `/resize`) to the tunnel handler with forced `__shell__` target and user-scoped agent access.
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

### 13. [Metrics](internal/metrics/AGENTS.md)
* **Responsibility**: Per-agent transport metrics collection (rolling window), BadgerDB persistence, auto-tuner that evaluates throughput saturation, latency, and error rate to push batch-size / concurrency / compression changes to agents via SSE `event: tune` control frames.
* **Path**: `internal/metrics/`

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
./local.sh server --metrics-dir=""               # server with metrics disabled
./local.sh agent --target 127.0.0.1:22            # agent proxying to local sshd
ssh -o ProxyCommand="./local.sh connect --local -" user@127.0.0.1   # SSH through tunnel
```

### Service Management
```bash
ssetunnel server start --target 127.0.0.1:22      # install + start as OS service
ssetunnel server stop                              # stop the service
ssetunnel server status                            # check service status
ssetunnel server reload                            # send SIGHUP for config reload
```

### Key Conventions
* **Test-first for bugs**: Always write a reproduction test before fixing. The `TestServeRW_ServerClosesReturns` test was written to prove the SSH ProxyCommand deadlock before the fix.
* **Bidirectional proxy pattern**: Every hop uses two goroutines with `io.CopyBuffer` + `sync.Pool` 32 KiB buffers. When one direction hits EOF, the corresponding connection/stream must be closed to unblock the other direction.
* **Fail-fast model**: POST failures, stream deaths, and pipe closures are terminal — sessions die and agents reconnect. No partial recovery.
* **No goroutine leaks**: Every goroutine hangs off a context owned by its conn/session. `Close()` always tears down cleanly.

---

## Environment Variables
| Variable | Used by | Purpose |
|----------|---------|----------|
| `DATABASE_URL` | server | PostgreSQL connection URL |
| `SSETUNNEL_RECOVERY_CODE_PEPPER` | server | HMAC key for recovery code digests (optional; SHA-256 fallback) |

## Known Pitfalls & Lessons Learned

### SSH ProxyCommand Deadlock (ServeRW)
**Problem**: When the remote target (e.g., sshd) closes after sending data (keyboard-interactive prompt), `ServeRW` waited for both `io.Copy` goroutines. The r→server copy blocked on `r.Read()` from stdin (os.Pipe), which the SSH client keeps open waiting for the prompt. Deadlock: SSH waits for data from proxy, proxy waits for stdin EOF from SSH.

**Fix**: Run server→w in the main goroutine and return immediately when the server side reaches EOF. The r→server goroutine becomes fire-and-forget; process exit (in ProxyCommand mode) handles cleanup. Closing `serverConn` does NOT unblock a blocking pipe read — the read is on a different FD.

**Key insight**: TCP half-close (`CloseWrite`) signals EOF to the remote side without killing the read path. But when the *other* side closes first, you must return from the proxy function to tear down the pipe, not wait for the blocked reader.

### yamux Stream Close Semantics
yamux `stream.Close()` kills both directions — there is no half-close. This means you cannot signal "done writing" while still reading. The HTTP transport has no half-close either — a full `Close()` on the connect client's `serverConn` is the only way to propagate upstream EOF.

### Agent Routing and Dynamic Target
Connect clients pass `agent` and `target` as HTTP query parameters on `GET /connect`. The server routes to the target agent via `findYamuxByAgentID` (or first-match if `agent` is empty). When `target` is provided and the agent wants target headers (`WantTarget`), the server writes it as the first line on the yamux stream.

**Agent configs** in the `agents` table define allowed targets per agent. A NULL `agent_id` row serves as the default config for all agents. The `TargetAllowed` function matches patterns like `*`, `host:*`, or `host:port`.

### SSH ProxyCommand Error Messages
**Problem**: When the connection fails, SSH only shows "Connection closed by UNKNOWN port 65535".

**Fix**: Write the error to both `os.Stderr` and `w` (stdout pipe) in `ServeRW` so SSH displays the actual error message before its generic message.

### pgx Array Scanning
**Problem**: The store uses pgx/v5 but `pq.Array()` from lib/pq is incompatible with pgx's binary protocol.

**Fix**: Scan `[]string` directly (pgx handles `text[]` natively) and use `time.Time` for timestamps (pgx returns binary timestamps, not text).

### Agent Reconnect Backoff
Sessions that survived past the 10 s health threshold reset the backoff to 0 — a drop after long uptime is a network event, not a flapping server, so retry immediately.

### Cloud Shell (`__shell__` Target)
The console frontend can open a browser-based shell. The server's `ShellConnectHandler` forces `target=__shell__` via a context key (bypassing agent config validation). The agent detects `__shell__` and spawns a local shell with PTY instead of dialing TCP. Resize events are sent as NUL-prefixed JSON on the yamux stream.

### Auto-Tuning
The `AutoTuner` evaluates agents every 30 s: throughput saturation adjusts batch size (4 KiB–1 MiB), p95 latency adjusts concurrency (1–4), and bandwidth level toggles gzip. Decisions are pushed via SSE `event: tune` control frames. Agents apply batch-size and compress changes live; concurrency changes are deferred to next reconnect. The tuner enforces a 2-minute cooldown between decisions per agent.

### Base Path Prefix
All tunnel endpoints support a `--base` flag (e.g. `--base /tunnel`) for reverse-proxy setups. The prefix is prepended to `/events`, `/up`, `/connect`, `/connect-up`, `/probe`. Both agent and connect client pass the same `--base` to match the server.
