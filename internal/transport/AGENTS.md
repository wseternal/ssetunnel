# Transport

Core SSE-downstream + batched-POST-upstream transport layer. Implements the wire protocol between agent and server.

## Architecture

```
Agent side (Conn)                    Server side (Handler + Session)
─────────────────                    ─────────────────────────────
Write(b) ─→ Batcher ─→ POST /up      handleUp → Session.push → up Pipe → yamux
                                                                Read
yamux → Session.Write → down Pipe → GET /events SSE → readLoop → down Pipe → Read
```

## Core Types

### `Config`
| Field | Type | Purpose |
|-------|------|---------|
| `URL` | `string` | Tunnel server base URL (required) |
| `BasePath` | `string` | HTTP path prefix prepended to all endpoints (e.g. `/tunnel`); empty = no prefix |
| `SessionID` | `string` | 128-bit random hex; auto-generated when empty |
| `Token` | `string` | Bearer token for agent authentication |
| `RequestModifier` | `func(*http.Request)` | Dynamic auth header injection (takes precedence over Token) |
| `OnTokenUpgrade` | `func(string)` | Called when server returns `X-SSET-Token` (PIN redemption) |
| `AgentID` | `string` | Human-readable agent identifier; sent as `X-SSET-Agent-ID` |
| `WantTargetHeader` | `bool` | Request server to write target address on yamux stream |
| `Target` | `string` | Dynamic target address passed as query parameter |
| `Concurrency` | `int` | Wanted POST sender depth; 0→1 (serial) |
| `Compress` | `bool` | Wanted gzip-per-batch; only on windowed sessions |
| `EventsPath` | `string` | Override SSE endpoint (default `/events`; `/connect` for DialConnect) |
| `UpPath` | `string` | Override POST endpoint (default `/up`; `/connect-up` for DialConnect) |

### `Conn` (agent-side `net.Conn`)
- **Downstream (Read)**: SSE `readLoop` decodes base64 frames → `Pipe` (256 KiB cap). Tune control frames (`event: tune`) are dispatched to `OnTune` callback instead of the pipe.
- **Upstream (Write)**: `Batcher` accumulates writes → serial `send()` or pool `submit()` → `POST /up`
- **Concurrency**: `pool chan upTask` bounded channel; full = backpressure to batcher
- **Gzip**: `gzipBatch` (BestSpeed) — only sends compressed when strictly smaller
- **Close**: Cancels context first (aborts in-flight POSTs), drains batcher, reaps transport idle conns
- **ApplyTune**: Adjusts batch size (via `Batcher.SetMaxSize`) and compression flag at runtime. Concurrency changes are deferred to reconnect (v1 limitation).
- **ErrUnauthorized**: Returned by `DialAgent` on 401 — unrecoverable, token is invalid.

### `Batcher`
Write coalescing with three flush triggers:
1. **Size**: Buffer reaches `maxSize` → immediate flush (no delay)
2. **Idle**: Sender idle + data buffered → eager flush (interactive traffic pays no delay)
3. **Timer**: Sender busy + data accumulating → flush at `maxWait` (25 ms default)

Backpressure: `Write` blocks when queued bytes ≥ `maxQueued` (4 MiB default).

### `Pipe`
Bounded in-memory byte pipe with deadline support. `io.Pipe` cannot express deadlines non-destructively; this can. Backed by `sync.Mutex` + cap-1 signal channels for reader/writer wakeup.

### `ReorderWindow`
Reassembles seq-numbered out-of-order batches (from concurrent POST workers). 8-slot window, piggybacked gap timeout (25 s). Pure data structure — no goroutines or timers.

### SSE Codec
- **Write**: `data: <base64>\n\n` + `Flush()`. Heartbeats: `: ka\n\n`.
- **Read**: `sseDecoder` — incremental line-based parser, max 1 MiB per line, base64 decode.
- **Tune frames**: `event: tune\ndata: <base64 JSON>\n\n` — `WriteTuneFrame` encodes, `readLoop` dispatches to `OnTune` callback.

### Capability Negotiation (`caps.go`)
Agent sends `X-SSET-Caps: concurrency=4;batch=16384;gzip`. Server responds with its caps. Both sides compute per-axis `min(want, have)` independently. Absent/malformed = fail closed to cycle-1 serial behavior.

## Wire Headers
| Header | Direction | Purpose |
|--------|-----------|---------|
| `X-SSET-Session` | Agent→Server | 128-bit hex session ID |
| `X-SSET-Seq` | Agent→Server | Monotonic batch sequence number |
| `X-SSET-Caps` | Both | Capability advertisement |
| `X-SSET-Flags` | Agent→Server | Per-batch flags (`gzip`) |
| `X-SSET-Token` | Server→Agent | PIN upgrade token |
| `X-SSET-Agent-ID` | Agent→Server | Agent routing identifier |
| `X-SSET-Target` | Agent→Server | Request target header on yamux stream |

## DialConnect
Connect clients use `DialConnect` which sets `EventsPath=/connect` and `UpPath=/connect-up`, requests `concurrency=4`. The server currently does not advertise caps on `/connect`, so negotiation fails closed to serial POSTs. Agent ID and target are passed as query parameters.

## Rules
* **Fail-fast**: Any POST failure kills the conn (sticky `sendErr`). Session death → agent reconnects.
* **POST body cap**: Server rejects bodies > `maxUpBody` (1 MiB + 64 KiB) with 413.
* **Gzip only on windowed sessions**: Server returns 400 for gzip flag on non-negotiated session.
* **No compression on transport layer**: `DisableCompression: true` — SSE must not be gzip-buffered.
* **BasePath**: Prepended to all endpoint paths (events + up). Used for reverse-proxy setups (`--base` flag).
* **RequestModifier**: When set, takes precedence over `Token` for auth header injection. Called before every HTTP request.
* **Auto-tune control frames**: `event: tune` SSE frames carry JSON-encoded `TransportParams`. The `readLoop` dispatches these to `OnTune` instead of the downstream pipe.
