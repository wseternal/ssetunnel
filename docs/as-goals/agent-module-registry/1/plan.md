# Iteration 1 Plan — Agent Module Registry

## Status
Phase 4, Step 1 is complete. This plan awaits user review. No implementation or test execution is part of this iteration.

## Critical Files
1. `internal/agent/agent.go` — owns module manager lifetime and preserves existing stream dispatch.
2. `internal/agent/control.go` — owns session-scoped control senders and module control dispatch.
3. `internal/server/session.go` — owns catalog epochs, pending invocations, and teardown.
4. `internal/server/server.go` — accepts agent-opened module streams after the control stream.
5. `frontend/console/vite.config.ts` — must support federation while it preserves single-file output and ES2022.

## Current Integration Facts
- `internal/mux/control.go` frames JSON control messages and caps each message at 64 KiB.
- `internal/agent/control.go` serializes control writes and handles agent-level messages before stream messages.
- `internal/server/server.go` currently accepts only the agent-opened control stream.
- `internal/agent/agent.go` uses raw fixed-target streams and header-based dynamic-target streams.
- `internal/transport/caps.go` ignores unknown capabilities and supports additive negotiation.
- `internal/server/remoteapp.go` demonstrates user checks before and after agent resolution.
- `internal/consoleserver/consoleserver.go` mounts authenticated routes before the SPA catch-all.
- `frontend/console/src/App.tsx` uses fixed tab indices and owns the current authentication helpers.

## Architecture Decisions

### 1. Separate Processes and Local IPC
Run one child process per provider module. Keep the supervisor outside the agent reconnect loop. Use Unix sockets on Unix and named pipes on Windows. Require owner-only endpoints, peer identity checks, and a random credential for each process launch. Pass credentials through inherited descriptors or protected files, never command arguments.

Treat modules as trusted installed code by default. Require distinct OS identities or a platform sandbox for mutually untrusted modules. IPC tokens alone do not isolate processes that share an OS identity.

### 2. One Public Go SDK
Use `github.com/wseternal/ssetunnel/sdk` for manifests, discovery, provider serving, consumer invocation, UI descriptors, and typed errors. Providers supply an OpenAPI v3 YAML document and a standard `http.Handler`. Consumers use agent, module, epoch, and revision references. Public types expose no yamux, SSE, IPC, or stream identifiers.

### 3. Additive Capability Negotiation
Add `modules=1` to the existing capability exchange. Send module messages only after mutual negotiation. Old peers retain their current byte protocols and return no module catalog. Do not add target headers to fixed-target streams.

### 4. Atomic Control-Plane Catalogs
Send full registry snapshots after each connection and each coalesced registry change. Include module identity, service version, revision, health, OpenAPI YAML, and UI descriptors. Use start, chunk, and commit messages with 32 KiB raw chunks. Keep the current 64 KiB control message cap.

Attach reassembly state and the committed catalog to one server `Session`. Use the transport session ID as the catalog epoch. Publish a new catalog only after digest and OpenAPI validation. Keep the previous committed catalog visible during an update. Reject invocations with an old revision.

Initial limits:
- 64 modules per agent.
- 1 MiB OpenAPI YAML per module.
- 4 MiB aggregate catalog data.
- Two OpenAPI validation workers with an eight-item queue.
- One active snapshot reassembly per session.

Interleave invocation and removal messages between catalog chunks. Never hold registry locks during network I/O.

### 5. Reverse-Opened HTTP Data Streams
For each invocation, the server sends `module_open` with an invocation ID, module ID, and revision. The agent validates the request and opens one yamux stream to the server. The agent writes a bounded module marker and invocation ID. The server correlates that stream with a bounded pending request.

Carry one HTTP/1.1 exchange with `Connection: close` on each stream. Use content-length or chunked bodies for request EOF. Do not rely on yamux half-close. Proxy the exchange to the provider over local IPC with standard HTTP parsers and streaming reverse-proxy behavior. Close the complete stream on cancellation, deadline, module failure, or session loss.

Initial limits:
- 32 pending opens per agent with a three-second establishment deadline.
- 32 active invocations per agent and eight per module.
- 64 KiB request headers, 8 KiB paths, and 16 KiB raw queries.
- Bounded local IPC connections and 32 KiB copy buffers.

### 6. Session-Scoped Authorization and Failure Semantics
Require console user authentication or an authorized consumer bearer token. Require connect permission and user-scoped agent access. Repeat the ownership check after agent resolution and before stream handoff. Resolve only accessible sessions and return `409` for ambiguous duplicate agent IDs.

Pin each invocation to agent, module, epoch, and revision. Return `409` for stale revisions and `503` for session loss. Never replay an invocation automatically. Consumers must rediscover after reconnect and choose any explicit retry policy.

Strip console credentials, cookies, reserved headers, and hop-by-hop headers before invocation. Match the method and normalized path against the committed OpenAPI document. Reject absolute URLs, dot segments, encoded path escapes, and excess metadata.

### 7. Trusted Federation Assets
Keep OpenAPI documents and UI descriptors in control snapshots. Transfer UI files through the HTTP data plane only after authorization. Serve all remotes from same-origin console asset routes.

Provider UI packages require an exact package digest in a server-local trust store. The server fetches provider assets through a declared HTTP asset operation. Server-consumer UI packages come from a server-local installed package directory. Both package types list every asset path, size, and SHA-256 digest. Verify every asset before cache storage or response delivery.

Initial UI limits:
- 8 MiB per asset.
- 64 MiB bounded server cache.
- Manifest-listed relative paths only.
- Digest-keyed cache entries with access checks on every request.

Remote JavaScript is trusted code, not sandboxed code. Do not accept arbitrary remote URLs. Do not expose raw console tokens to remotes. Supply a narrow host API with authenticated fetch and invocation functions.

### 8. Minimal Console Integration
Use Vite Module Federation runtime registration for discovered remotes. Share React and ReactDOM as singleton dependencies. Preserve the single-file host build and `build.target: 'es2022'`. Prove this combination before full UI integration.

Append module tabs after existing tabs and add hash-based module routes. Do not migrate the whole console to a new router. Add error boundaries, load timeouts, retry controls, revision reloads, and abort cleanup.

### 9. Ephemeral Catalogs and Persistent Desired State
Keep installed packages and desired process state in the agent data directory. Keep server catalogs in memory, not PostgreSQL. Restart enabled modules after an agent process restart. Rebuild server catalogs from current agent snapshots after reconnect. Never restore stale server registry state.

## Ordered Task List

### Task 1: Establish the Public SDK Contract
**Files, 5:** `sdk/manifest.go`, `sdk/discovery.go`, `sdk/errors.go`, `sdk/doc.go`, `sdk/types_test.go`.

- Define manifests, health, service references, epochs, revisions, UI descriptors, and typed errors.
- Define RFC problem-details mapping for transport and registry failures.
- Keep all wire and IPC details private.

**Acceptance:** External providers and consumers can share the contracts without internal imports.

**Verification:** Compile two external contract examples and review `go doc ./sdk` for transport leakage.

### Task 2: Define the Wire Protocol and Capability
**Files, 5:** `internal/moduleproto/protocol.go`, `internal/moduleproto/limits.go`, `internal/transport/caps.go`, `internal/transport/conn.go`, `internal/moduleproto/protocol_test.go`.

- Add `modules=1`, snapshot messages, open messages, error codes, markers, and limits.
- Expose the negotiated module capability to agent and server session code.

**Acceptance:** Old peers ignore the capability, and new peers send no module messages without mutual support.

**Verification:** Exercise codec boundaries, malformed input, absent capabilities, and both mixed-version directions.

### Task 3: Validate Packages and OpenAPI Documents
**Files, 5:** `internal/agentmod/package.go`, `internal/agentmod/openapi.go`, `internal/agentmod/package_test.go`, `go.mod`, `go.sum`.

- Validate module IDs, versions, revisions, executables, package paths, and asset digests.
- Parse OpenAPI 3.0 and 3.1 YAML offline with maintained pinned dependencies.
- Reject duplicate YAML keys, duplicate operations, external references, excess depth, and oversized input.
- Hash trusted UI assets during installation.

**Acceptance:** Invalid packages cannot register or reference files outside their package roots.

**Verification:** Use real files and documents for valid, malformed, duplicate, traversal, external-reference, and limit cases.

### Task 4: Add Secure Local IPC
**Files, 5:** `internal/agentmod/ipc.go`, `internal/agentmod/ipc_unix.go`, `internal/agentmod/ipc_windows.go`, `internal/agentmod/client.go`, `internal/agentmod/ipc_test.go`.

- Implement owner-only Unix sockets and Windows named pipes.
- Authenticate peers and bind registrations to supervised process identity and launch credentials.
- Provide a small local admin and registration client for CLI and SDK adapters.

**Acceptance:** Unauthorized local processes cannot register, replace, or control a module.

**Verification:** Use real sockets, pipes, and helper processes for peer, token, spoof, and disconnect cases.

### Task 5: Implement the Registry and Process Supervisor
**Files, 5:** `internal/agentmod/registry.go`, `internal/agentmod/supervisor.go`, `internal/agentmod/supervisor_unix.go`, `internal/agentmod/supervisor_windows.go`, `internal/agentmod/supervisor_test.go`.

- Track desired state, live registration, health, package revision, and process handles.
- Start executables directly without a shell and capture bounded logs.
- Deregister on process exit or registration disconnect.
- Stop with a graceful deadline, then kill the process group.
- Use jittered restart backoff for enabled modules.

**Acceptance:** Start, stop, restart, and crash affect only the selected module.

**Verification:** Kill real child processes and verify deregistration, restart, cleanup, and unrelated module survival.

### Task 6: Add Local CLI Lifecycle Commands
**Files, 5:** `cmd/ssetunnel/main.go`, `cmd/ssetunnel/module.go`, `internal/agentmod/install.go`, `cmd/ssetunnel/module_test.go`, `cmd/ssetunnel/AGENTS.md`.

- Add `module install`, `start`, `stop`, `restart`, `status`, and `remove`.
- Use atomic package installation and require stop before removal.
- Restore enabled modules after an agent process restart.
- Support server-local consumer UI and trust package installation without a server restart.

**Acceptance:** Agent module lifecycle commands require no agent rebuild or restart.

**Verification:** Operate a real example module through the CLI and verify persistent desired state and absent-agent errors.

### Task 7: Implement the Provider SDK
**Files, 5:** `sdk/provider.go`, `sdk/provider_unix.go`, `sdk/provider_windows.go`, `sdk/provider_test.go`, `examples/module-provider/main.go`.

- Expose a small `Serve` API for a manifest and standard `http.Handler`.
- Hide service listener setup, registration, health updates, and deregistration.
- Serve declared UI assets through the manifest's asset operation.

**Acceptance:** A provider author supplies only package configuration, OpenAPI YAML, and an HTTP handler.

**Verification:** Build and run a real provider through local IPC for unary, streamed, health, and shutdown cases.

### Task 8: Publish Agent Catalog Snapshots
**Files, 5:** `internal/agent/module.go`, `internal/agent/module_sync.go`, `internal/agent/agent.go`, `internal/agent/control.go`, `internal/agent/module_test.go`.

- Start the module manager once outside the reconnect loop.
- Publish a fresh full snapshot after every connection and coalesced registry change.
- Interleave priority control messages between chunks and use session-scoped senders.

**Acceptance:** Modules survive tunnel reconnects, and only current registry state returns.

**Verification:** Reconnect during registration and snapshot transfer. Confirm bounded queues and stale-sender rejection.

### Task 9: Add Session-Scoped Server Catalogs
**Files, 5:** `internal/server/module_catalog.go`, `internal/server/module_control.go`, `internal/server/session.go`, `internal/server/module_catalog_test.go`, `internal/server/compat_test.go`.

- Reassemble and validate snapshots with bounded workers.
- Replace catalogs atomically and scope them to the transport session epoch.
- Clear reassembly, catalogs, and pending module state during session teardown.

**Acceptance:** Incomplete or stale snapshots never become visible or affect replacement sessions.

**Verification:** Race reconnect, update, removal, validation, and discovery against real session cleanup.

### Task 10: Implement Agent Invocation Streams
**Files, 5:** `internal/agent/module_invoke.go`, `internal/agent/module_http.go`, `internal/agent/agent.go`, `internal/agent/control.go`, `internal/agent/module_invoke_test.go`.

- Handle bounded `module_open` requests and validate module revisions.
- Open marked reverse streams and proxy one streamed HTTP exchange over local IPC.
- Enforce invocation limits and cancel local work on stream closure.

**Acceptance:** Slow or failed modules do not block existing TCP, shell, desktop, or unrelated module streams.

**Verification:** Exercise empty, large, bidirectional, canceled, deadline, crash, and overload cases with real processes.

### Task 11: Implement Server Stream Acceptance and Cleanup
**Files, 5:** `internal/server/module_stream.go`, `internal/server/module_http.go`, `internal/server/server.go`, `internal/server/session.go`, `internal/server/module_stream_test.go`.

- Accept agent-opened module streams after control-stream establishment.
- Correlate bounded pending IDs and enforce handshake deadlines.
- Close pending and active invocations on session death or server shutdown.

**Acceptance:** Unknown markers, stale IDs, timeouts, and reconnect races leave no goroutines or map entries.

**Verification:** Race open, cancel, timeout, replacement, and shutdown over real yamux sessions.

### Task 12: Expose Authorized Discovery and Invocation APIs
**Files, 5:** `internal/consoleserver/module_routes.go`, `internal/consoleserver/consoleserver.go`, `internal/server/module_access.go`, `internal/server/module_http.go`, `internal/consoleserver/module_routes_test.go`.

- Add catalog, OpenAPI YAML, invocation, and UI descriptor routes before console catch-alls.
- Enforce user scope, connect permission, epoch, revision, and OpenAPI operation matching.
- Strip credentials and unsafe headers before invocation.
- Clear the request read deadline only for authorized streaming invocations through `http.ResponseController`.

**Acceptance:** Cross-agent, ambiguous, stale, and reconnect-race requests fail closed without global timeout changes.

**Verification:** Exercise all HTTP methods, queries, headers, streamed bodies, long requests, and authorization races.

### Task 13: Implement the Consumer SDK
**Files, 5:** `sdk/consumer.go`, `sdk/http.go`, `sdk/consumer_test.go`, `sdk/sdk_integration_test.go`, `examples/module-consumer/main.go`.

- Implement discovery, contract download, invocation, streaming, and typed error decoding.
- Return normal module HTTP responses without full-body buffering.
- Perform no hidden invocation retries.

**Acceptance:** The external consumer imports only the public SDK and standard library.

**Verification:** Run real discovery, unary, streamed, cancellation, stale-revision, and typed-error examples.

### Task 14: Harden the End-to-End HTTP Data Plane
**Files, 5:** `internal/server/e2e_test.go`, `internal/server/module_http_test.go`, `internal/agent/agent_test.go`, `internal/agent/module_test.go`, `internal/transport/module_e2e_test.go`.

- Cover content-length, chunked, early-response, and bidirectional body behavior.
- Cover slow readers, slow writers, cancellation, deadlines, reconnects, and module crashes.
- Run existing target and mixed-version regression cases without mocks.

**Acceptance:** Every case terminates with bounded resources and no goroutine or stream leaks.

**Verification:** Run the real SSE-plus-POST transport suite and the existing agent and server regression suites.

### Task 15: Add Trusted UI Package Stores
**Files, 5:** `internal/moduleui/manifest.go`, `internal/moduleui/store.go`, `internal/moduleui/assets.go`, `internal/moduleui/store_test.go`, `cmd/ssetunnel/module.go`.

- Implement provider trust entries and server-consumer UI packages with exact package digests.
- Accept only listed relative asset paths with matching size and SHA-256.
- Reload atomically installed server-local packages without a server restart.

**Acceptance:** Agent metadata cannot introduce an arbitrary origin, unapproved package, or changed asset.

**Verification:** Reject traversal, digest mismatch, unknown revisions, oversized assets, and unauthorized package changes.

### Task 16: Add Console Page Discovery and Asset Routes
**Files, 5:** `internal/consoleserver/moduleui.go`, `internal/consoleserver/consoleserver.go`, `internal/server/module_http.go`, `internal/moduleui/assets.go`, `internal/consoleserver/moduleui_test.go`.

- Merge authorized provider pages and trusted server-consumer pages.
- Proxy provider assets through the declared operation and serve consumer assets from local packages.
- Use bounded digest caches and repeat access checks on every request.

**Acceptance:** Unapproved or changed assets never reach the browser, and remote failures return explicit errors.

**Verification:** Exercise provider and consumer assets, cross-user requests, revision changes, cache bounds, and failure recovery.

### Task 17: Build the Federation Host
**Files, 5:** `frontend/console/package.json`, `frontend/console/bun.lock`, `frontend/console/vite.config.ts`, `frontend/console/src/modules/ModuleHost.tsx`, `frontend/console/src/App.tsx`.

- Add runtime federation registration, singleton React dependencies, and server-generated remote URLs.
- Append module tabs and hash routes without a full router migration.
- Provide a narrow authenticated host API, error boundaries, timeout, retry, and revision reload behavior.
- Preserve single-file output and ES2022.

**Acceptance:** Existing tabs retain their behavior, and failed remotes do not crash the host or create a second React runtime.

**Verification:** Build the host and inspect remote imports, shared dependencies, the ES2022 target, and the prior xterm enum failure pattern.

### Task 18: Add Provider and Consumer UI Examples
**Files, 5:** `examples/module-provider/openapi.yaml`, `examples/provider-ui/vite.config.ts`, `examples/provider-ui/src/App.tsx`, `examples/consumer-ui/vite.config.ts`, `examples/consumer-ui/src/App.tsx`.

- Provide one streamed operation, one typed error, and two federation pages.
- Use the same public SDK and host API as external module authors.
- Include unavailable-remote and retry behavior.

**Acceptance:** Both examples build and demonstrate provider and consumer page flows.

**Verification:** Build both remotes and run real backend examples. Request permission before browser validation.

### Task 19: Add Diagnostics and Resource Metrics
**Files, 5:** `internal/metrics/module.go`, `internal/server/module_metrics.go`, `internal/agent/module.go`, `internal/server/module_http.go`, `docs/adr/002-agent-module-registry.md`.

- Record catalog size, sync duration, pending opens, active invocations, bytes, errors, cancellations, and UI failures.
- Add opaque correlation IDs across consumer, server, agent, and module.
- Redact tokens, cookies, authorization headers, bodies, queries, and OpenAPI content.
- Record the reverse-open and trust decisions in the ADR.

**Acceptance:** Operators can diagnose overload, stale revisions, reconnects, and remote failures without secret disclosure.

**Verification:** Inspect real logs and metrics during success, cancellation, saturation, reconnect, crash, and asset rejection.

### Task 20: Complete Documentation and Gate Verification
**Files, 5:** `docs/guide/002-agent-modules.md`, `sdk/doc.go`, `README.md`, `CHANGELOG.md`, `AGENTS.md`.

- Document package format, trust model, provider and consumer SDK examples, limits, and failure semantics.
- Document mixed versions, deployment, rollback, and UI asset compatibility.
- Produce all seven gate evidence files with linked source and command results.

**Acceptance:** All gates have evidence, and the release notes cover every changed binary and asset type.

**Verification:** Run the permitted full backend suite and all builds. Request permission before any browser verification.

## Dependencies
- Tasks 1 and 2 define contracts before implementation.
- Tasks 3 through 6 precede provider registration and agent publication.
- Tasks 7 through 9 precede invocation.
- Tasks 10 and 11 precede public invocation routes.
- Task 12 precedes the consumer SDK and provider asset proxying.
- Task 14 precedes any release decision.
- Tasks 15 and 16 precede federation host integration.
- Tasks 17 and 18 precede browser verification.
- Tasks 19 and 20 complete the operations gate.
- No database migration is required.
- Full backend verification may require Docker for existing TestContainers suites.

## Gate Traceability

| Gate | Primary tasks |
|------|---------------|
| Dynamic Module Lifecycle | 3–8 |
| OpenAPI Discovery | 1–3, 8–9, 12 |
| HTTP Invocation Data Plane | 2, 7, 10–14 |
| Unified Go SDK | 1, 7, 13, 18, 20 |
| Console Module Federation | 15–18 |
| Security and Isolation | 3–5, 9–16 |
| Operations and Compatibility | 2, 8–14, 19–20 |

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Fixed-target framing regression | Use reverse-opened streams. Never sniff or modify existing target bytes. |
| Catalog traffic delays control messages | Use bounded chunks, coalesced snapshots, interleaved priority messages, and write deadlines. |
| YAML or metadata memory exhaustion | Enforce limits before allocation and parsing. Disable external references and bound validation workers. |
| HTTP deadlock or resource leak | Use standard streaming parsers, full-stream cancellation, bounded maps, and cleanup verification. |
| Stale-session invocation after reconnect | Pin session epoch and revision. Clear all module state from `Session.Close`. |
| Duplicate agent IDs route across users | Resolve accessible sessions first and reject ambiguity with `409`. |
| Duplicate side effects after failure | Never replay invocations automatically. Require fresh discovery and explicit caller policy. |
| Same-UID module compromise | Require separate OS identities or a platform sandbox for mutually untrusted modules. |
| Console credentials reach providers | Strip credentials and reserved headers. Expose only a narrow host API to remotes. |
| Federation supply-chain compromise | Require exact trusted package digests, same-origin assets, and per-file verification. |
| Federation breaks the single-file host | Prove the build before UI integration. Pin compatible versions and preserve ES2022. |
| Long streams hit console timeouts | Clear only the authenticated invocation read deadline. Retain all other server limits. |
| Busy transport delays cancellation | Retain bounded queues and 32 KiB buffers. Measure cancellation before considering stream pools. |

## Rejected Alternatives
- **In-process Go plugins:** They violate process isolation and dynamic process lifecycle requirements.
- **Loopback TCP IPC:** It exposes ports and provides weaker local peer identity than sockets or named pipes.
- **Server-opened target-header streams:** Fixed-target streams lack headers, and byte inspection can corrupt server-first protocols.
- **Module invocation through `/connect`:** It mixes authorization models and risks reserved-target bypass.
- **A custom RPC IDL or body frame format:** It duplicates HTTP semantics and the OpenAPI contract.
- **Buffered invocation bodies:** Buffering breaks streaming and adds avoidable memory pressure.
- **A persistent global server catalog:** It creates stale-session reconciliation and cross-user risks.
- **Arbitrary federation remote URLs:** They bypass package trust and expose console credentials.
- **A full React Router migration:** It increases regression risk without improving module isolation.
- **Pre-opened invocation pools:** They add reconnect bursts and idle streams before measurements justify them.
- **Automatic retry or agent load balancing:** It can duplicate non-idempotent operations and exceeds the scope.

## Review Stop
Stop after this document. The Engineer must not start Task 1 until the user approves the goal, gates, and plan.
