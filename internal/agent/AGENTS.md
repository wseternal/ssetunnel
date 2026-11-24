# Agent

The restricted-network agent. Dials out to the tunnel server over HTTP, multiplexes yamux streams, and proxies each to a local TCP target or spawns a local shell with PTY.

## Architecture

```
Run(ctx)
  │  reconnect loop with exponential backoff (500 ms → 30 s, 10% jitter)
  ▼
runOnce(ctx)
  │
  ├── transport.DialAgent() — opens SSE /events, negotiates caps
  ├── conn.OnTune callback — parses event: tune JSON, applies batch/compress changes
  ├── mux.Client(conn) — yamux client session
  └── AcceptStream loop
        │
        └── proxy(stream)
              ├── Static target: net.DialTimeout(target)
              ├── Dynamic target: read target from stream header, then dial
              ├── Shell target (__shell__): proxyShell(stream) — PTY + local shell
              └── two goroutines: io.Copy(target↔stream)
```

## Core Type: `Agent`

```go
type Agent struct {
    ServerURL       string        // tunnel server base URL
    Target          string        // TCP address to forward streams to (empty = dynamic target mode)
    AgentID         string        // agent identifier for routing
    Token           string        // Bearer token or single-use PIN
    MaxBackoff      time.Duration // reconnect cap; 0 → 30 s
    MaxWait         time.Duration // batcher flush ceiling; 0 → default
    Client          *http.Client  // nil → transport default
    RequestModifier func(*http.Request) // session-based auth header injector
    BatchSize       int           // upstream batch ceiling
    Concurrency     int           // upstream POST sender depth
    Compress        bool          // negotiate gzip-per-batch
    NoAutoTune      bool          // disable server auto-tuning
    OnTokenRefresh  func() (string, error) // mid-lifecycle token refresh callback
}
```

**Dynamic target mode**: When `Target` is empty, the agent reads the target address from the yamux stream header (first `\n`-terminated line) and connects to that address for each stream. The `readerConn` wrapper preserves any bytes buffered beyond the header line for the subsequent `io.Copy`.

**Shell target**: When the stream header is `__shell__`, the agent spawns a local shell (`$SHELL` or `/bin/sh`) as a **login shell** (argv[0] prefixed with `-`) with a PTY via `proxyShell`. This sources login startup files (`.zprofile`, `.profile`, `/etc/profile`) in addition to interactive startup files (`.zshrc`, `.bashrc`), matching `sshd` behavior. The data stream is a pure byte pipe; PTY resize messages arrive on the separate control stream (`mux.MsgResize`, routed by yamux stream ID), never interleaved with keystrokes.

## Reconnect Strategy

`Run` loops `runOnce` with exponential backoff (500 ms → 30 s cap, 2× multiplier, 10% jitter) via `cenkalti/backoff`. Sessions surviving past the 10 s health threshold reset the backoff — a drop after long uptime is a network event, not a flapping server.

**Mid-lifecycle token refresh**: Before each reconnect attempt, `Run` calls `RefreshToken()` which invokes `OnTokenRefresh` (if set). The callback checks `NeedsRefresh` from the session file, calls `auth.RefreshSession` + `auth.UpdateSessionToken`, and returns the new token. On success, `Agent.Token` is updated; on failure, the current token is kept and the reconnect proceeds.

**Unrecoverable errors** (`transport.ErrUnauthorized`): Exit immediately — the token is invalid and retrying will not help.

## Auto-Tuning

When `NoAutoTune` is false, the agent installs a `conn.OnTune` callback that:
1. Parses `event: tune` JSON frames (`TransportParams{concurrency, batch_size, compress}`)
2. Applies batch-size and compress changes immediately via `conn.ApplyTune`
3. Concurrency changes are deferred to next reconnect (v1 limitation)

## Proxy

`proxy(stream)` determines the target (static, dynamic from header, or `__shell__`), then:
- **TCP target**: Dials with 10 s timeout, runs two goroutines: `io.Copy(target, stream); target.Close()` and `io.Copy(stream, target); stream.Close()`. Both close their "other" connection on EOF.
- **Shell target**: Calls `proxyShell(stream)` which spawns a PTY-backed shell and proxies bidirectionally.

## Rules
* **Never hold references across reconnect cycles** — `Close()` reaps idle transport connections.
* **PIN upgrade**: `OnTokenUpgrade` callback updates `Agent.Token` when the server returns a persistent token via `X-SSET-Token`.
* The agent generates a 128-bit random session ID per connection.
* **AgentID** identifies the machine for server-side routing — required when multiple agents are registered.
* **Dynamic target mode** reads target from stream header; rejects empty or `*` targets.
* **`NoAutoTune`**: When true, `event: tune` frames are ignored; agent keeps its static CLI flags.

## Control Stream: Log Retrieval

The agent captures recent `log.Printf` output into a bounded ring buffer (`logBuffer`, 256 lines) via `io.MultiWriter(os.Stderr, logBuf)`. The server retrieves these lines on demand via `MsgLogRequest`/`MsgLogResponse` on the control stream.

- **Request**: server sends `MsgLogRequest` with `request_id` (random hex) and `lines` count
- **Response**: agent reads up to `lines` from `logBuffer.Read(lines)` and sends back as `MsgLogResponse`
- **Timeout**: server has a 10s timeout; old agents that don't support log retrieval silently drop the request

## Control Stream: Tracing (Reserved)

`Agent.SendTrace(streamID, eventType, ...)` sends diagnostic trace events via the control stream (`MsgTrace`). Non-blocking: drops if the control stream is unavailable (logs a warning on first drop per control stream cycle). Reserved for future shell and data stream lifecycle instrumentation.

Shell sessions can enable verbose tracing (`Agent.Verbose`) which adds lifecycle and byte-count logging via `tracedReader`/`tracedWriter`/`tracedCopy` wrappers.

## Modules

`InitModules(ctx, dataDir)` builds the registry, IPC server, supervisor, and module manager. It applies `Agent.ModuleProbeTimeout` to the registry, then scans the directory next to the data directory (`~/.ssetunnel/modules` for the default data dir) with `Registry.ScanModules`, which registers and enables every installed package. `ReloadModules` (SIGHUP from the CLI) reloads the registry, rescans that directory, and reconciles the supervisor.

After the first `Supervisor.Reconcile()`, `InitModules` starts the local module API (`module_api.go`). A failure there is logged and does not abort module startup: the tunnel path keeps working without the local surface.

### Object store forwarding (`objectstore.go`)

`InitModules` creates the forwarders before the IPC listener, then installs `SetOnObjectStore` beside `SetOnDataStore`. The handler unmarshals the IPC request, overwrites `module_id` with the authenticated IPC identity, clears any caller-supplied `request_id`, and calls `Agent.ObjectStoreRequest`.

- `objectStoreForwarder` owns correlation IDs: it generates a random `request_id`, rejects a duplicate already pending, and fragments a put with `moduleproto.NewObjectStoreDataChunks`.
- A get sends one request. The forwarder reassembles bounded response chunks with `moduleproto.ObjectStoreAssembler` and ignores a response whose non-empty `module_id` does not match the pending request.
- Every pending entry stores its control generation. A stale stream cannot satisfy or clear a newer entry; a monotonic closed-generation watermark rejects requests that try to register after their stream closed.
- The operation bound is two minutes, or the caller context deadline when shorter.
- Cleanup paths: terminal response, malformed response, timeout, control stream close (`FailGeneration`), `Agent.Close` (`FailAll` before the IPC server closes), and a control write error. `controlSender` returns the write error, so a dead stream fails the operation at once instead of waiting for the timeout.

### Invocation paths

`ModuleInvoker` carries two entry points that share admission, resolution, and teardown:

| Entry | Stream | Used by |
|-------|--------|---------|
| `Invoke(ctx, moduleID, revision, open)` | yamux data stream | the tunnel, driven by the server's module-open messages |
| `InvokeLocal(ctx, moduleID, revision, req)` | none (nil) | the loopback API |

Both go through `admitLocked`, which checks and increments `activeInvocations`/`perModuleCounts` inside one critical section and returns an idempotent release closure (`pendingOpens` is guarded in the same section so a close cannot interleave with an admit). `Invocation.finish` is a `sync.Once` that cancels the context, closes the module connection and — only when non-nil — the yamux stream, drops the registry entry, and releases the slot. `InvokeLocal` additionally applies `ModuleInvocationTimeout` (30 s) to the module request and wraps the response body so closing it runs `finish` exactly once.

Sentinels returned to callers: `ErrModuleNotFound`, `ErrModuleRevisionMismatch`, `ErrModuleInvokerClosed`, `ErrModuleCapacity`. `Close()` marks the invoker closed, drains the tracked invocations, and leaves the counters untouched (they are only read by admission, which now refuses everything).

### Local module API (`module_api.go`)

A second HTTP surface, bound to `127.0.0.1:0`, that serves the same route shapes as the console so a local caller needs no token, no session, and no tunnel:

- `/api/v1/modules?agent=<id>`, `/api/v1/modules/{agent}/{module}` (metadata), `/api/v1/modules/openapi/{agent}/{module}`, `/api/v1/modules/{agent}/{module}/operations`
- `/api/v1/modules/invoke/{agent}/{module}[/{path...}]` (both route forms; the operation path comes from `moduleapi.TrailAfterSegments`)
- `/api/v1/modules/ui/{agent}/{module}`, `/api/v1/modules/ui/provider/...`, `/api/v1/modules/ui/consumer/...`

Boundary (`localBoundary`): loopback peer address, `Host` equal to the listener address (DNS-rebinding guard), `Origin` if present must be exactly `http://<addr>`, `Sec-Fetch-Site` must be `same-origin`/`none`, and CORS preflights are refused — this surface does not opt into CORS. Agent identity: a configured `AgentID` is required; with none configured, `local` is the accepted name and an omitted `agent` parameter passes.

The catalog epoch is process-scoped (`local-<hex>`, never the tunnel session ID), returned as `X-Catalog-Epoch` and optionally enforced with `?epoch=` (mismatch → `409`). Invocation validates the operation with `agentmod.ValidateOpenAPI` + `MatchOperation` before dialing, then maps failures to `404`/`409`/`429`/`503`/`502`.

`Agent.Close()` tears down in order: module API (shutdown, bounded), module invoker, supervisor, IPC. It is idempotent and returns the first error. `ModuleAPIURL()` exposes the published base URL to `clientapp`, which writes it into the agent status file so `cli ui` can find it.

## Rules
* Module request/response policy lives in `internal/moduleapi` — don't re-derive header filtering or operation-path handling here.
* The local API must never be reachable from the tunnel listener; it is a separate loopback listener owned by the agent process.
* Object store pending entries must fail on control stream loss and `Agent.Close`. A module must never wait out its full timeout during teardown.
