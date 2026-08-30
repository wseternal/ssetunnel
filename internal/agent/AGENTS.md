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
    BasePath        string        // HTTP path prefix (e.g. "/tunnel")
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

**Shell target**: When the stream header is `__shell__`, the agent spawns a local shell (`$SHELL` or `/bin/sh`) as a **login shell** (argv[0] prefixed with `-`) with a PTY via `proxyShell`. This sources login startup files (`.zprofile`, `.profile`, `/etc/profile`) in addition to interactive startup files (`.zshrc`, `.bashrc`), matching `sshd` behavior. PTY resize messages are NUL-prefixed JSON parsed from the stream.

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
