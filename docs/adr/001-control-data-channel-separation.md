# ADR-001: Separate Control and Data Channels on yamux

## Status
Accepted

## Date
2026-10-28

## Context

The cloud shell feature exhibited a freeze when running vim — keystrokes would stop reaching the PTY while output continued. Multiple debugging attempts failed to isolate the root cause because the tunnel lacked structured observability into what the agent was doing in real time.

The existing design multiplexed control commands (PTY resize) alongside application data on the same yamux stream using an in-band NUL-byte + length-prefixed JSON protocol. This made it impossible to:

1. Add diagnostic tracing without further polluting the data stream
2. Send agent-to-server control messages (e.g., trace events) cleanly
3. Extend the control vocabulary without risking data corruption

A doubt-driven review established that stream separation alone does **not** fix the vim freeze — the freeze vectors are session-level (shared batcher, yamux `ConnectionWriteTimeout`, SSE backpressure). However, separation is a prerequisite for the structured observability needed to **diagnose** the freeze.

## Decision

Introduce a dedicated **control yamux stream** per agent session, alongside the existing data streams. The control stream carries structured JSON messages (resize, trace, future commands) with a simple wire format. Data streams become pure byte pipes with no in-band signalling.

### Wire Format

- Control stream opens with marker line: `__control__\n`
- Messages: 4-byte big-endian length prefix followed by JSON body
- Message types: `resize` (server→agent), `trace` (bidirectional), extensible
- Stream correlation via yamux's built-in `StreamID()` (uint32)

### Architecture

```
Agent  ──[control stream]──▶ Server   (agent opens; resize, trace, future commands)
Server ──[data stream 1]──▶  Agent   (pure shell PTY bytes)
Server ──[data stream 2]──▶  Agent   (pure TCP forwarding bytes)
Server ──[data stream N]──▶  Agent   (pure remote desktop bytes)
```

- **Per-agent, not per-data-stream**: one control stream serves all data streams for that agent
- **Agent-initiated**: the agent opens the control stream in `runOnce()` after the yamux session is established, writes the `__control__\n` marker. The server's `attach()` accepts it asynchronously.
- **Clean separation**: data streams are accepted by the agent without any header inspection (fixed mode) or with a single-line target header read (dynamic mode). No peeking or byte-level stream detection needed.

### Key Code Locations

| Component | File | Role |
|-----------|------|------|
| Protocol types | `internal/mux/control.go` | `ControlMessage`, `ReadControlMessage`, `WriteControlMessage`, message type constants |
| Agent control handler | `internal/agent/control.go` | `streamRegistry`, `handleControlStream`, `dispatchControlMessage`, `SendTrace` |
| Agent accept loop | `internal/agent/agent.go` | `openControlStream()` opens control stream; `acceptStream()` handles data streams only |
| Server stream setup | `internal/server/server.go` | `attach()` accepts the agent-opened control stream, verifies marker |
| Server control API | `internal/server/session.go` | `SetControlStream()`, `SendControl()` (thread-safe) |
| Shell resize | `internal/server/shell_session.go` | `resizeForwarder` sends via `agentSess.SendControl()` |

## Alternatives Considered

### In-band control on data streams (status quo)
- **Pros**: No additional stream overhead, simpler accept loop
- **Cons**: NUL-byte protocol fragile (user-generated NUL bytes), no clean path for agent→server messages, adding tracing increases data stream complexity
- **Rejected**: Observability requirements demand a clean bidirectional control channel

### Per-data-stream control channels
- **Pros**: No need for stream ID correlation, simpler routing
- **Cons**: Doubles the number of yamux streams per connection, control messages can't be sent when no data stream is open, agent must manage N control readers
- **Rejected**: Unnecessary overhead; one control stream per agent is sufficient

### HTTP-based control API (separate from yamux)
- **Pros**: Decoupled from yamux lifecycle, standard REST semantics
- **Cons**: Requires separate HTTP connection from agent (may not traverse proxies), adds latency, doesn't share yamux session fate
- **Rejected**: Agent is behind HTTP-only proxy; the control channel must ride the same transport as data

## Consequences

### Positive
- Data streams are pure byte pipes — no NUL-byte parsing, no risk of control/data corruption
- Resize moved off the data stream — shell sessions have a single writer (inputForwarder), eliminating the `writeMu` mutex
- Extensible: adding new control message types (trace, config push, health checks) requires only a new type constant and handler
- Thread-safe `SendControl()` allows any server component to send control messages
- Agent `streamRegistry` enables targeted delivery by yamux stream ID
- Agent-initiated control stream avoids complex stream-type detection on the agent side

### Negative
- One additional yamux stream per agent session (minimal overhead — yamux streams are cheap)
- Breaking change: no backward compatibility with agents expecting the NUL-byte resize protocol (acceptable — dev phase)

### Risks
- Control stream failure kills resize/tracing for the session but does not affect data streams (by design — control is supplementary)
- If the agent's `openControlStream` fails (e.g. session dies before stream opens), the server's `attach()` Accept call returns an error and the control stream is unavailable for that session

## What's Still To Come
- Agent-side diagnostic tracing (PTY I/O timing, shell lifecycle events) sent via control stream
- Server-side trace collection (ring buffer per session)
- Console API endpoint for fetching traces
- Console frontend diagnostics viewer (command palette action)
