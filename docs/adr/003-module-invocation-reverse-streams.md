# ADR-003: Module Invocation via Reverse yamux Streams

## Status
Accepted

## Date
2026-11-02

## Context

Once the agent's module registry (ADR-002) exposes pluggable HTTP services, the server must proxy HTTP requests from console users (or external SDK consumers) through the tunnel to those modules. The challenge:

- The agent is behind NAT/firewall — the server cannot open connections to it
- The existing yamux data streams use a "server opens, agent accepts" pattern with optional target headers
- Module invocations must support full HTTP/1.1 (streaming bodies, chunked encoding)
- Multiple concurrent invocations per module must not corrupt each other
- The invocation path must not bypass existing auth or agent routing

The existing `/connect` endpoint already proxies TCP through yamux, but it authorizes against reserved targets (SSH, databases) and uses a different stream framing.

## Decision

Use **reverse-opened yamux streams** for module invocations: the server sends a `module_open` control message to the agent, the agent opens a new yamux stream back to the server, writes a marker, and the server correlates it with the pending request. One HTTP/1.1 exchange per stream with `Connection: close`.

### Invocation Flow

```
Console/SDK → Server (module_routes.go)
  1. Auth check + find agent session
  2. Validate operation against OpenAPI contract
  3. Send module_open control message {module_id, invocation_id, revision, deadline}
  4. Wait for reverse stream (3 s establishment deadline)

Server → Agent (control stream)
  5. Agent receives module_open
  6. Agent opens new yamux stream to server
  7. Agent writes "__module__\n<invocationID>\n" marker
  8. Agent dials fresh IPC connection to module's invocation socket

Server (module_stream.go)
  9. Correlate stream with pending invocation by invocationID
  10. Write HTTP request to yamux stream
  11. Read HTTP response from yamux stream
  12. Filter response headers, stream body back to caller
```

### Why Reverse-Opened Streams

The agent opens the stream toward the server (not the other way around) because:

- **NAT traversal**: the agent always initiates yamux streams — the server cannot push
- **Clean framing**: no need to sniff bytes to determine stream type (the marker line identifies it)
- **Consistency with ADR-001**: the control stream established the "agent opens, server accepts" pattern

### Concurrency Controls

TOCTOU-safe slot reservation prevents over-subscription:

| Limit | Value | Scope |
|-------|-------|-------|
| Pending opens | 32 | Per agent |
| Active invocations | 32 | Per agent |
| Per-module invocations | 8 | Per module |

Slots are reserved atomically **before** releasing the lock for the stream open, and released on failure. This prevents a race where N concurrent requests all pass the check, then all try to open streams.

### Cancellation Safety

Each `PendingInvocation` carries both a `ResponseCh` (for the stream) and a `Done` channel (for cancellation). Cancellation signals `Done` without closing `ResponseCh`, preventing send-on-closed-channel panics that occurred in earlier iterations.

### Header Security

Before writing the HTTP request to the yamux stream, the proxy strips:

- **Auth headers**: `Authorization`, `Cookie`, `X-SSE-Token` (prevents credential forwarding to modules)
- **Hop-by-hop headers**: `Connection`, `Keep-Alive`, `Transfer-Encoding`, `Proxy-*`
- **Response sensitive headers**: `Set-Cookie`, `Connection`, `Transfer-Encoding`

### Marker Protocol

The agent writes `__module__\n<invocationID>\n` as the first bytes on the reverse stream. The server reads this marker via `bufio.Reader`, which preserves buffered HTTP request bytes for subsequent `http.ReadRequest`.

### Key Code Locations

| Component | File | Role |
|-----------|------|------|
| Agent invocation handler | `internal/agent/module_invoke.go` | HandleModuleOpen, proxyInvocation, slot reservation |
| Server stream handler | `internal/server/module_stream.go` | RequestInvocation, DeliverStream, CancelInvocation |
| Console invocation proxy | `internal/consoleserver/module_routes.go` | handleInvoke with header filtering |
| Wire format | `internal/moduleproto/protocol.go` | ModuleOpenPayload, message types |
| Capability negotiation | `internal/transport/caps.go` | `modules=1` capability flag |

## Alternatives Considered

### Server-opened target-header streams
- **Pros**: Simpler — server opens stream, writes target header, done
- **Cons**: Fixed-target streams lack headers (the agent accepts them without header inspection). Adding headers to these streams would require byte-sniffing, which corrupts server-first protocols.
- **Rejected**: Reverse-opened streams with explicit markers avoid framing ambiguity

### Module invocation through `/connect`
- **Pros**: Reuse existing proxy infrastructure
- **Cons**: `/connect` authorizes against reserved targets — mixing module invocation bypasses target ACLs. Different stream framing (target header vs module marker). Modules and TCP targets share the yamux session but have fundamentally different authorization models.
- **Rejected**: Separate invocation path preserves security boundaries

### Custom RPC IDL / body frame format
- **Pros**: Type-safe, compact
- **Cons**: Duplicates HTTP + OpenAPI semantics. Modules already expose HTTP APIs with OpenAPI specs. Adding a frame format means every SDK must implement it.
- **Rejected**: Standard `http.ReadRequest` / `http.ReadResponse` parsers handle streaming bodies without custom framing

### Buffered invocation bodies
- **Pros**: Simpler error handling, retry support
- **Cons**: Breaks streaming (large uploads, server-sent events). Adds memory pressure for large bodies. Modules that expect streaming input would deadlock.
- **Rejected**: One HTTP/1.1 exchange per stream with live body streaming

### Pre-opened invocation stream pools
- **Pros**: Lower latency (no stream setup round-trip)
- **Cons**: Reconnect bursts fill the pool with dead streams. Idle streams waste yamux window. No measurements justify the complexity yet.
- **Rejected**: Measure first, optimize if needed. Current 3 s establishment deadline is sufficient.

### Automatic retry / agent load balancing
- **Pros**: Transparent failure recovery
- **Cons**: Non-idempotent operations (POST, DELETE) cannot be safely retried. Load balancing across agents requires consistent hashing or session affinity.
- **Rejected**: Clients decide retry policy; the proxy is a dumb pipe

## Consequences

### Positive
- Reverse-opened streams work through NAT without port forwarding
- One stream per invocation — no cross-request corruption
- Full HTTP/1.1 support including streaming request/response bodies
- `module_open` → reverse-stream pattern is consistent with ADR-001 control/data separation
- Concurrency limits prevent resource exhaustion on both agent and server
- Header stripping prevents credential leakage to untrusted modules

### Negative
- One yamux stream round-trip per invocation adds ~1 RTT latency
- `Connection: close` means no connection reuse — each invocation pays TCP setup cost within yamux
- 3 s establishment deadline may be too tight for agents on very high-latency links
- No connection pooling means burst traffic creates burst stream opens

### Risks
- A module that hangs forever blocks its yamux stream — the 30 s default context deadline mitigates this, but callers must set their own deadlines for long operations
- If the agent disconnects mid-invocation, the server's pending request times out (3 s) rather than returning immediately — acceptable for the current scale
- The `module_open` control message is fire-and-forget (control sender has no error return) — failures surface as establishment timeouts
