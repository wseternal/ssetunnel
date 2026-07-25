# SSH ProxyCommand Deadlock & Half-Close in Pipe Proxies

## Deadlock Visualization: BEFORE (Broken)

```
┌──────────────┐         stdin pipe          ┌──────────────┐        TCP/yamux        ┌──────────┐
│  SSH Client  │ ──────── (fd 0) ──────────▶│  ServeRW     │ ───────────────────────▶│  sshd    │
│              │◀─────── (fd 1) ──────────── │  (proxy)     │◀─────────────────────── │  :22     │
└──────────────┘         stdout pipe         └──────────────┘                         └──────────┘
                                                                        │
                                                              sends "Password: "
                                                              then CLOSES ──┐
                                                                           ▼
                                                              ┌────────────────────────┐
                                                              │  server→w goroutine:    │
                                                              │  io.Copy returns (EOF)  │
                                                              │  ─────────────────────  │
                                                              │  WAITS for r→server     │
                                                              │  goroutine to finish    │
                                                              │  (sync.WaitGroup)       │
                                                              └───────────┬────────────┘
                                                                          │
                                                              ┌───────────▼────────────┐
                                                              │  r→server goroutine:    │
                                                              │  io.Copy blocked on     │
                                                              │  r.Read() ← stdin pipe  │
                                                              │  ─────────────────────  │
                                                              │  SSH client keeps stdin │
                                                              │  OPEN (waiting for      │
                                                              │  "Password: " prompt)   │
                                                              └────────────────────────┘

  ┌──────────────────────────────────────────────────────────────────────────────────────────┐
  │                             D E A D L O C K                                             │
  │                                                                                          │
  │   SSH Client          waits for "Password: " from stdout    ← stdout pipe open          │
  │   r→server goroutine  waits for stdin EOF (r.Read)          ← stdin pipe open           │
  │   ServeRW             waits for r→server goroutine          ← sync.WaitGroup.Wait()     │
  │                                                                                          │
  │   Nobody makes progress. Hangs forever.                                                  │
  └──────────────────────────────────────────────────────────────────────────────────────────┘
```

## Deadlock Visualization: AFTER (Fixed)

```
┌──────────────┐         stdin pipe          ┌──────────────┐        TCP/yamux        ┌──────────┐
│  SSH Client  │ ──────── (fd 0) ──────────▶│  ServeRW     │ ───────────────────────▶│  sshd    │
│              │◀─────── (fd 1) ──────────── │  (proxy)     │◀─────────────────────── │  :22     │
└──────────────┘         stdout pipe         └──────────────┘                         └──────────┘
                                                                        │
                                                              sends "Password: "
                                                              then CLOSES ──┐
                                                                           ▼
                                                              ┌────────────────────────┐
                                                              │  server→w (MAIN goroutine)│
                                                              │  io.Copy returns (EOF)  │
                                                              │  ─────────────────────  │
                                                              │  RETURNS immediately.   │
                                                              │  ServeRW exits.         │
                                                              │  Process tears down.    │
                                                              │  stdout pipe closes ────┼──▶ SSH sees EOF
                                                              └────────────────────────┘
                                                                           │
                                                              ┌────────────▼───────────┐
                                                              │  r→server (background): │
                                                              │  fire-and-forget.       │
                                                              │  Abandoned on return.   │
                                                              │  Process exit reaps it. │
                                                              └────────────────────────┘

  ┌──────────────────────────────────────────────────────────────────────────────────────────┐
  │                             R E S O L V E D                                             │
  │                                                                                          │
  │   1. sshd sends "Password: " and closes                                                 │
  │   2. ServeRW server→w gets EOF → returns immediately                                    │
  │   3. Process exits → stdout pipe closes                                                 │
  │   4. SSH client reads "Password: " from stdout, then sees EOF → proceeds                │
  │   5. r→server goroutine is leaked but process exit cleans up (acceptable for            │
  │      ProxyCommand where the process IS the proxy)                                       │
  └──────────────────────────────────────────────────────────────────────────────────────────┘
```

## Key Insight

```
  Closing serverConn (TCP) does NOT unblock r.Read() on stdin pipe
  ─────────────────────────────────────────────────────────────────
  They are DIFFERENT file descriptors. The r→server goroutine is
  blocked on the PIPE, not on the TCP connection. You must return
  from the proxy function to let the process exit close the pipe.
```

---

## Why Normal SSH Doesn't Have This Problem

### Normal SSH (no proxy)

```
Normal SSH:  SSH Client ────── TCP socket ────── sshd
```

There is **no proxy process**. The SSH client and sshd communicate over a single bidirectional TCP socket. When sshd sends data and closes its write side (FIN), the SSH client's `read()` returns the data, then `read()` returns 0 (EOF). The SSH client is free to proceed. No deadlock is possible because there's no intermediate process.

### SSH via ProxyCommand (with proxy)

```
ProxyCommand:  SSH Client ←── pipes ──→ Proxy Process ←── network ──→ sshd
```

The proxy process has **two independent I/O channels**: stdin/stdout (to SSH client) and the server connection (to sshd). Closing one does NOT close the other. The proxy must **decide** when to tear down. This is a fundamental property of ANY pipe-based proxy — `nc`, `socat`, `proxychains`, all face the same issue.

### The deadlock root cause is at the proxy process level, not yamux

```
Root cause:  ServeRW waited for BOTH io.Copy goroutines (WaitGroup)
             One was blocked on stdin (never returns)
             The other finished (server closed)
             → Deadlock regardless of what transport is underneath
```

This would happen even with a **direct TCP connection** (no yamux at all). Any bidirectional pipe proxy that waits for both directions has this problem. The fix — return when server→stdout finishes — solves it at the process level, independent of the transport.

---

## Yamux Half-Close: A Separate Architectural Constraint

Yamux's half-close limitation is a **separate architectural constraint** that affects protocol fidelity (can't propagate directional close through the tunnel), but it didn't cause the deadlock.

### The half-close gap in the chain

```
Entry TCP ────── yamux stream ────── Agent ────── Target TCP
(supports         (NO half-close      (uses Close,    (supports
 CloseWrite)       CloseWrite)         kills both)     CloseWrite)
```

In a pure TCP world, when sshd sends data and closes its write side, the FIN signal travels back through the TCP connection. The client sees data + EOF and can still send data back (if needed). This is **TCP half-close**.

In ssetunnel, the yamux layer is a **hard half-close barrier**. `stream.Close()` sends a RST flag that kills both directions. There is no `CloseWrite` equivalent.

### Impact on protocols

- **SSH works** because sshd does a full close (not half-close) and SSH client doesn't need half-close semantics
- **HTTP/1.0, FTP data, some DB protocols** may NOT work faithfully because they rely on TCP half-close to signal "I'm done writing, now waiting for your response"

---

## What Yamux Provides (Why We Keep It)

Yamux's job in ssetunnel is **stream multiplexing** — allowing multiple independent user connections to share a single tunnel.

### Without yamux (1 tunnel = 1 connection)

```
  User A ──TCP──→ Entry ──HTTP tunnel──→ Agent ──TCP──→ sshd   (tunnel 1)
  User B ──TCP──→ Entry ──HTTP tunnel──→ Agent ──TCP──→ mysql  (tunnel 2)
  User C ──TCP──→ Entry ──HTTP tunnel──→ Agent ──TCP──→ sshd   (tunnel 3)
                  ↑                    ↑
            3 separate tunnels     3 separate tunnels
            3 SSE connections      3 agent processes
```

### With yamux (1 tunnel = many connections)

```
  User A ──TCP──→ Entry ─┐
  User B ──TCP──→ Entry ─┼── yamux ──HTTP tunnel──→ Agent ──TCP──→ sshd
  User C ──TCP──→ Entry ─┘         (1 tunnel)       Agent ──TCP──→ mysql
                  ↑                                   Agent ──TCP──→ sshd
            3 users, 1 tunnel, 3 yamux streams
```

### Feature comparison

| Feature | With yamux | Without yamux |
|---------|-----------|---------------|
| Multiple users per agent | Yes — N streams over 1 tunnel | No — 1 tunnel per connection |
| Connection setup cost | Open a yamux stream (microseconds) | New HTTP tunnel (SSE + POST setup, ~100ms+) |
| Bandwidth sharing | Shared window across streams | Isolated per tunnel |
| Keepalive | 1 SSE heartbeat serves all streams | N SSE heartbeats |
| Reconnect | 1 reconnect heals all streams | N reconnects |
| Cycle-2 concurrency | N parallel POSTs, reorder window | Per-tunnel only |

### Why yamux was chosen

Yamux multiplexes over a **plain `io.ReadWriteCloser`**, which fits perfectly over the SSE-down + POST-up transport that traverses restrictive HTTP-only proxies. QUIC and HTTP/2 would break that design goal.

### Could you replace yamux and keep multiplexing?

| Alternative | Half-close? | Trade-off |
|-------------|------------|-----------|
| **QUIC streams** | Yes (via `CloseWrite`) | Much more complex, no HTTP-only proxy compatibility |
| **Custom framing over HTTP** | Yes (encode FIN in frame header) | Reimplement multiplexing from scratch |
| **HTTP/2 streams** | Yes (RST_STREAM vs half-close) | Loses the "plain outbound HTTP" design goal |

---

## Summary

```
The deadlock:      proxy process design issue → FIXED (return on server EOF)
Yamux half-close:  architectural limitation → KNOWN (can't propagate FIN through tunnel)
Dropping yamux:    lose multiplexing → 1 tunnel per connection (much more expensive)
```

The deadlock fix stands regardless of whether you keep yamux. The half-close gap is real but only matters for protocols that depend on TCP half-close — SSH doesn't, which is why the fix works.
