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

### `Conn` (agent-side `net.Conn`)
- **Downstream (Read)**: SSE `readLoop` decodes base64 frames → `Pipe` (256 KiB cap)
- **Upstream (Write)**: `Batcher` accumulates writes → serial `send()` or pool `submit()` → `POST /up`
- **Concurrency**: `pool chan upTask` bounded channel; full = backpressure to batcher
- **Gzip**: `gzipBatch` (BestSpeed) — only sends compressed when strictly smaller
- **Close**: Cancels context first (aborts in-flight POSTs), drains batcher, reaps transport idle conns

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

## Rules
* **Fail-fast**: Any POST failure kills the conn (sticky `sendErr`). Session death → agent reconnects.
* **POST body cap**: Server rejects bodies > `maxUpBody` (1 MiB + 64 KiB) with 413.
* **Gzip only on windowed sessions**: Server returns 400 for gzip flag on non-negotiated session.
* **No compression on transport layer**: `DisableCompression: true` — SSE must not be gzip-buffered.
