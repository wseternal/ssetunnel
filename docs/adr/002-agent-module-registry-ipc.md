# ADR-002: Agent Module Registry and IPC Protocol

## Status
Accepted

## Date
2026-11-02

## Context

The tunnel agent runs behind restrictive HTTP-only proxies and forwards TCP traffic to local targets. Users needed a way to extend the agent with pluggable HTTP services (databases, internal APIs, custom tools) without rebuilding or redeploying the agent binary. Key requirements:

- **Process isolation**: modules must not crash or compromise the agent
- **Dynamic lifecycle**: install, enable, disable, and uninstall at runtime
- **Cross-platform**: Linux, macOS, and Windows
- **Secure**: modules run with the agent's network access and must be authenticated
- **Discoverable**: the server and console must know which modules are available and healthy

The existing control channel (ADR-001) provided a communication path between server and agent, but no mechanism for agent-to-local-process communication.

## Decision

Introduce a **module registry** that manages external module processes as children of the agent. Each module is a standalone binary that communicates with the agent over a **local IPC protocol** using newline-terminated text frames on Unix domain sockets (Linux/macOS) or named pipes (Windows).

### IPC Protocol

The protocol is a simple text-line state machine:

```
Module Process → Agent IPC Server
1. AUTH <cred_len>\n<credential_bytes>     ← authenticate
2. ← OK\n                                  ← accepted
3. REG <moduleID> <svcName> <ver> <rev> [invEndpoint]\n  ← register
4. HEALTH <status> [message]\n (loop)      ← periodic health
```

- **Authentication**: a random 32-byte hex credential generated per IPC server instance, passed to module processes via the `SSETUNNEL_MODULE_IPC_CREDENTIAL` environment variable. Never passed as a command argument (visible in `ps`).
- **Owner-only endpoints**: Unix sockets created with mode `0600`; named pipes with owner-only ACLs.
- **Message size cap**: 64 KiB per line, enforced by `bufio.NewReaderSize`.
- **Phase timeouts**: 5 s for authentication, 10 s for registration, 60 s health read deadline.

### Dual-Socket Architecture

Each module uses two IPC connections:

1. **Control connection**: long-lived, carries AUTH/REG/HEALTH messages
2. **Invocation socket**: `<endpoint>-inv`, accepts fresh connections per HTTP invocation

Fresh connections per invocation avoid shared-connection corruption when multiple HTTP requests are proxied concurrently.

### Lifecycle Management

A **Supervisor** manages module processes independently of the tunnel reconnect loop:

- **Desired state persistence**: `desired_state.json` tracks enabled/disabled modules atomically (write-to-temp + rename). Modules auto-restore on agent restart.
- **Crash recovery**: jittered exponential backoff (500 ms base, 30 s cap, ±25% jitter via `crypto/rand`). Exponent capped at 6 to prevent int64 overflow.
- **Lock-release-before-sleep**: the supervisor releases its mutex before `time.After` in backoff, re-validates state after wake. Prevents 30 s shutdown stalls.
- **Graceful shutdown**: SIGTERM → 5 s grace → SIGKILL. Process group killing ensures child cleanup on Unix.

### Module Identity

- **Module ID**: reverse-DNS format (`com.example.myapi`), max 128 chars
- **Service name**: human-readable, max 128 chars
- **Version**: semver, max 32 chars
- **Revision**: content hash for cache-busting, max 64 chars

### Key Code Locations

| Component | File | Role |
|-----------|------|------|
| IPC server | `internal/agentmod/ipc.go` | Protocol handler, credential auth, dual-socket |
| Platform sockets | `internal/agentmod/ipc_unix.go`, `ipc_windows.go` | Unix socket / named pipe creation |
| Registry | `internal/agentmod/registry.go` | Package tracking, desired state, supervisor |
| Package validation | `internal/agentmod/package.go` | ID format, path safety, symlink rejection |
| Module manager | `internal/agent/module_manager.go` | Snapshot publication, update coalescing |
| Protocol types | `internal/moduleproto/protocol.go` | Wire message types and constants |
| Protocol limits | `internal/moduleproto/limits.go` | All capacity bounds |
| Provider SDK | `sdk/provider.go` | Module-side dual-socket helper |

## Alternatives Considered

### In-process Go plugins (`plugin` package)
- **Pros**: No IPC overhead, direct function calls
- **Cons**: No process isolation (plugin panic kills agent), no dynamic unload, requires exact Go version match, Linux/macOS only
- **Rejected**: Violates process isolation and cross-platform requirements

### Loopback TCP IPC
- **Pros**: Simple, cross-platform, well-understood
- **Cons**: Exposes ports (even on loopback), weaker peer identity than Unix sockets, TCP overhead for local communication
- **Rejected**: Unix sockets provide kernel-level peer identity with no port exposure

### gRPC over Unix sockets
- **Pros**: Strong typing, code generation, streaming support
- **Cons**: Heavy dependency (protobuf, grpc), overkill for simple register/health protocol, complicates module SDK for third-party developers
- **Rejected**: The protocol is simple enough that text-line frames suffice; HTTP over yamux handles streaming

### Shared IPC connection per module
- **Pros**: Fewer connections, simpler state management
- **Cons**: Concurrent HTTP invocations share one connection, causing response interleaving and corruption
- **Rejected**: Fresh connection per invocation (current design) eliminates cross-request corruption

### Supervisor inside reconnect loop
- **Pros**: Simpler code structure
- **Cons**: Module processes die on every tunnel reconnect, defeating the purpose of stable services
- **Rejected**: Modules must survive tunnel disconnects

## Consequences

### Positive
- Modules run as isolated processes — crashes, memory leaks, and hangs don't affect the agent
- Text-line protocol is trivial to implement in any language (no SDK dependency required)
- Credential-based auth prevents unauthorized local processes from registering
- Desired state persistence provides "install once, run forever" semantics
- Dual-socket design separates control plane from data plane cleanly
- Jittered backoff prevents thundering herd on agent restart with many modules

### Negative
- Process management adds complexity (process groups, signal forwarding, zombie cleanup)
- Environment variable credential passing is visible to child processes
- Cross-platform IPC (Unix sockets + named pipes) requires platform-specific code paths
- 64 KiB message cap means OpenAPI documents must be sent via snapshot, not IPC

### Risks
- Module processes that ignore SIGTERM will be SIGKILLed after 5 s — data loss possible if modules don't flush on SIGTERM
- The credential is a single shared secret for all modules on one agent; a compromised module can impersonate others to the agent (acceptable — same trust domain)
