# Agent

The restricted-network agent. Dials out to the tunnel server over HTTP, multiplexes yamux streams, and proxies each to a local TCP target.

## Architecture

```
Run(ctx)
  │  reconnect loop with exponential backoff
  ▼
runOnce(ctx)
  │
  ├── transport.DialAgent() — opens SSE /events, negotiates caps
  ├── mux.Client(conn) — yamux client session
  └── AcceptStream loop
        │
        └── proxy(stream)
              ├── net.DialTimeout(target) — connect to local service
              └── two goroutines: io.Copy(target↔stream)
```

## Core Type: `Agent`

```go
type Agent struct {
    ServerURL       string        // tunnel server base URL
    Target          string        // TCP address to forward streams to (empty = dynamic target mode)
    AgentID         string        // agent identifier for routing
    Token           string        // Bearer token or single-use PIN
    RequestModifier func(*http.Request) // session-based auth header injector
    MaxBackoff      time.Duration // reconnect cap; 0 → 1 s
    BatchSize       int           // upstream batch ceiling
    Concurrency     int           // upstream POST sender depth
    Compress        bool          // negotiate gzip-per-batch
}
```

**Dynamic target mode**: When `Target` is empty, the agent reads the target address from the yamux stream header (`X-SSET-Target`) and connects to that address for each stream.

## Reconnect Strategy

`Run` loops `runOnce` with exponential backoff (50 ms → `MaxBackoff`, doubles each failure). Sessions surviving past the 10 s health threshold reset the backoff — a drop after long uptime is a network event, not a flapping server.

**Unrecoverable errors** (`transport.ErrUnauthorized`): Exit immediately — the token is invalid and retrying will not help.

## Proxy

`proxy(stream)` dials the target address (static or from stream header) with a 10 s timeout, then runs two goroutines:
- `io.Copy(target, stream); target.Close()`
- `io.Copy(stream, target); stream.Close()`

Both goroutines close their "other" connection on EOF, ensuring the stream and target are fully torn down when either side closes.

**Dynamic target**: When `Target` is empty, the agent reads `X-SSET-Target` from the stream's header and validates it against the agent config's `allowed_targets` before connecting.

## Rules
* **Never hold references across reconnect cycles** — `Close()` reaps idle transport connections.
* **PIN upgrade**: `OnTokenUpgrade` callback updates `Agent.Token` when the server returns a persistent token via `X-SSET-Token`.
* The agent generates a 128-bit random session ID per connection.
* **AgentID** identifies the machine for server-side routing — required when multiple agents are registered.
* **Dynamic target mode** reads target from stream header and validates against `allowed_targets`.
