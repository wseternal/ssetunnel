# Local Review main..feat/agent-module-registry — Request Changes

**Readiness Score: 0/100** (Fundamental issues — reconsider approach)
**Pre-Flight:** build=✅ tests=✅ vet=✅
**Agents dispatched:** Architect, Logician, Sentinel, Performer, Cartographer, Observer, Tracer
**Scope:** 38 files, 6,304 lines added across 10 commits

## Findings Summary

| Severity | Count | Group Risk |
|----------|-------|------------|
| Critical | 17 | **High** — IPC protocol stub, data corruption, deadlocks, security bypasses |
| Warning | 21 | **High** — Synchronization gaps, missing tests, observability voids |
| Suggestion | 10 | Medium — Coupling, dead code, performance polish |
| Nit / FYI | 4 | Low — Documentation clarity, naming |

---

## Critical Issues (MUST FIX)

### C1. IPC acceptLoop is a no-op stub — entire module invocation path is dead
[ipc.go#L70-L87](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agentmod/ipc.go#L70-L87) — *Flagged by: Architect, Sentinel, Performer, Observer, Tracer*

**Risk:** High — The entire provider↔agent registration and invocation path is non-functional.

**Problem:** `acceptLoop` accepts connections and immediately discards them (`_ = conn`). It never reads AUTH, never sends "OK\n", never processes REG or HEALTH. The SDK Provider's `authenticate()` blocks forever waiting for the "OK\n" response that never comes. `ipc.Lookup()` always returns nil → all invocations fail with "module not found". Every accepted connection leaks file descriptors.

**Fix:** Implement the full IPC protocol handler:
```go
func (s *IPCServer) acceptLoop(ln net.Listener) {
    for {
        conn, err := ln.Accept()
        if err != nil { /* existing error handling */ }
        go s.handleConnection(conn)
    }
}

func (s *IPCServer) handleConnection(conn net.Conn) {
    conn.SetReadDeadline(time.Now().Add(5 * time.Second))
    // 1. Read AUTH frame, verify credential
    // 2. Write "OK\n" or close
    // 3. Read REG message, call s.Register()
    // 4. Read HEALTH messages in loop
}
```

---

### C2. SDK `singleConnListener` serves exactly one HTTP request, then kills the module
[provider.go#L147-L171](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/sdk/provider.go#L147-L171) — *Flagged by: Architect, Observer*

**Risk:** High — A module process can serve exactly one invocation before dying.

**Problem:** `singleConnListener.Accept()` returns the connection once (`l.used = true`), then returns `"listener closed"`. `http.Server.Serve()` exits, killing the module. The comment acknowledges: *"In a production implementation, this would use a more sophisticated protocol."*

**Fix:** Use a connection-pooling approach or `net.Pipe` per invocation. At minimum, keep returning the same connection until the server is explicitly stopped.

---

### C3. Shared IPC `net.Conn` across concurrent invocations corrupts HTTP responses
[module_invoke.go#L93-L95](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agent/module_invoke.go#L93-L95) — *Flagged by: Tracer*

**Risk:** High — Silent data corruption between concurrent invocations.

**Problem:** `HandleModuleOpen` uses `reg.Conn` — a single `net.Conn` shared across all invocations to a module. With `MaxActiveInvocationsPerModule = 8`, concurrent goroutines write requests and read responses on the same connection. HTTP/1.1 on raw `net.Conn` is ordered: goroutine A reads goroutine B's response.

**Fix:** Open a fresh IPC connection per invocation, or serialize with a per-module mutex:
```go
type Registration struct {
    DialIPC func() (net.Conn, error) // fresh connection per invocation
}
```

---

### C4. `publishSnapshotLocked` holds mutex during blocking network I/O
[module_manager.go#L170-L236](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agent/module_manager.go#L170-L236) — *Flagged by: Architect, Logician, Performer*

**Risk:** High — Stalled control stream blocks all module manager operations and agent reconnect.

**Problem:** Holds `m.mu` while calling `m.sendControl()` repeatedly. During this time `SetControlSender`, `OnConnect`, `OnRegistryChange` all block. If the control stream stalls, the entire module manager freezes for up to 5s × N chunks.

**Fix:** Build messages under lock, send outside lock:
```go
msgs := m.buildPublishMessages(snapshot) // under lock
m.mu.Unlock()
for _, msg := range msgs { m.sendControl(msg) }
m.mu.Lock() // re-acquire
```

---

### C5. Nil dereference in `publishCoalescedUpdate` — panic on early registry change
[module_manager.go#L108](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agent/module_manager.go#L108) — *Flagged by: Logician*

**Risk:** High — Agent panic if module installed before first tunnel connection.

**Problem:** `snapshot.Revision = m.snapshot.Revision + 1` panics if `m.snapshot` is nil. Race: `OnRegistryChange` fires (timer starts) before `OnConnect` completes.

**Fix:** Guard: `revision := 0; if m.snapshot != nil { revision = m.snapshot.Revision }`

---

### C6. Supervisor deadlock: holds mutex during 30s backoff sleep
[registry.go#L320-L343](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agentmod/registry.go#L320-L343) — *Flagged by: Logician, Performer, Tracer*

**Risk:** High — 30-second shutdown stall, blocks all Reconcile/Stop operations.

**Problem:** `monitorProcess` acquires `s.mu.Lock()`, then `select { case <-time.After(backoff): ... }` with backoff up to ~30s. During this window, `Stop()` and `Reconcile()` block on the lock.

**Fix:** Release lock before sleeping, re-acquire and re-validate after:
```go
s.mu.Unlock()
select {
case <-time.After(backoff):
case <-s.ctx.Done():
    return
}
s.mu.Lock()
```

---

### C7. TOCTOU race on per-module invocation limit
[module_invoke.go#L70-L130](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agent/module_invoke.go#L70-L130) — *Flagged by: Logician*

**Risk:** Medium — Per-module limit of 8 can be bypassed by concurrent module_open requests.

**Problem:** Per-module count check and increment span separate lock sections with network I/O (`session.OpenStream()`) in between. Multiple concurrent requests all pass the check before any increments.

**Fix:** Reserve the per-module slot before releasing the lock, release on failure.

---

### C8. `CancelInvocation`/`DeliverStream` race — send on closed channel → panic
[module_stream.go#L164-L221](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/module_stream.go#L164-L221) — *Flagged by: Tracer*

**Risk:** High — Server panic under concurrent cancel + deliver.

**Problem:** `DeliverStream` retrieves pending, releases lock. `CancelInvocation` acquires lock, deletes, `close(pending.ResponseCh)`. `DeliverStream` sends on closed channel → panic.

**Fix:** Use a `done` channel per pending invocation; do not close `ResponseCh`.

---

### C9. Symlink traversal bypass in package validation — arbitrary code execution
[package.go#L141-L147](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agentmod/package.go#L141-L147) — *Flagged by: Sentinel*

**Risk:** High — Malicious package can execute arbitrary binaries via symlinks.

**Problem:** `isSubPath` uses `filepath.Rel` which does not resolve symlinks. A symlink `evil-bin → /usr/bin/sudo` passes the check but executes outside the sandbox.

**Fix:** Use `filepath.EvalSymlinks` on both parent and child. Reject symlinked executables via `os.Lstat`.

---

### C10. Missing Linux peer identity verification
[ipc_unix.go#L54-L64](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agentmod/ipc_unix.go#L54-L64) — *Flagged by: Sentinel*

**Risk:** Medium (Linux only) — Any user's process can connect if socket is accessible.

**Problem:** `VerifyPeerIdentity` returns nil on all platforms. On Linux, `SO_PEERCRED` provides kernel-verified UID/GID.

**Fix:** Implement `SO_PEERCRED` check on Linux. Keep macOS/BSD stub.

---

### C11. Nil pointer dereference in `CacheAsset`
[store.go#L179-L180](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/moduleui/store.go#L179-L180) — *Flagged by: Performer*

**Risk:** Medium — Panic when caching a new (not-yet-cached) asset.

**Problem:** `oldAsset := s.cache[cacheKey]` returns nil for new entries; `newUsed := s.cacheUsed - oldAsset.Size` panics.

**Fix:** `var oldSize int64; if oldAsset != nil { oldSize = oldAsset.Size }`

---

### C12. O(n²) allocation in snapshot commit
[module_catalog.go#L223-L226](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/module_catalog.go#L223-L226) — *Flagged by: Performer*

**Risk:** Medium — Latency spikes during large catalog syncs (~50MB allocations for 10MB catalog).

**Problem:** `data = append(data, chunk...)` re-allocates on each iteration without pre-allocation.

**Fix:** `data := make([]byte, 0, mc.reassembly.TotalSize)`

---

### C13. Goroutine leak in `proxyInvocation` — context never used
[module_invoke.go#L139-L179](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agent/module_invoke.go#L139-L179) — *Flagged by: Performer*

**Risk:** High — Goroutine holds stream and invocation slot forever if remote hangs.

**Problem:** `ctx` parameter is never used. `http.ReadRequest` and `http.ReadResponse` block indefinitely.

**Fix:** Set read deadlines from context or use a default timeout.

---

### C14. Zero test coverage on data plane modules
Multiple files — *Flagged by: Observer*

**Risk:** High — 5 critical files (module_invoke.go, module_stream.go, module_catalog.go, module_routes.go, store.go) totaling ~1,474 lines have zero tests.

**Fix:** Add table-driven tests for happy path, timeouts, chunk reassembly, and path traversal.

---

### C15. Metrics infrastructure defined but never wired
[metrics/module.go](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/metrics/module.go) — *Flagged by: Observer*

**Risk:** Medium — 18 atomic counters and 6 recording methods exist but zero code paths call them.

**Fix:** Wire `RecordInvocation()`, `RecordBytes()`, `RecordError()` into the invocation and catalog paths.

---

### C16. SDK `Consumer.Cancel()` targets non-existent endpoint
[discovery.go#L237-L259](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/sdk/discovery.go#L237-L259) — *Flagged by: Architect, Cartographer*

**Risk:** Medium — Public SDK method always returns 404.

**Fix:** Either implement the cancel endpoint on the server, or remove `Cancel()` from the SDK.

---

### C17. Marker read may consume HTTP request data
[server.go#L200-L219](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/server.go#L200-L219) — *Flagged by: Architect, Logician*

**Risk:** High — First bytes of HTTP request lost, corrupting the invocation.

**Problem:** Single `stream.Read(marker)` may return marker + beginning of HTTP request. Extra bytes beyond `\n` are silently discarded.

**Fix:** Use `bufio.Reader` to read exactly the marker line, preserving buffered bytes.

---

## Warnings (SHOULD FIX)

| # | Issue | File | Agent |
|---|-------|------|-------|
| W1 | `handleModuleHealth` mutates committed catalog under read lock | [module_catalog.go#L285-L310](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/module_catalog.go#L285-L310) | Architect |
| W2 | `OnRegistryChange` captures caller's context in timer closure | [module_manager.go#L78-L89](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agent/module_manager.go#L78-L89) | Architect, Logician |
| W3 | Orphaned validation queue/workers never used | [module_catalog.go#L85-L92](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/module_catalog.go#L85-L92) | Architect, Cartographer, Performer |
| W4 | CORS wildcard on invoke endpoint | [module_routes.go#L174-L179](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/consoleserver/module_routes.go#L174-L179) | Sentinel |
| W5 | Environment variable inheritance leaks secrets to modules | [supervisor_unix.go#L17](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agentmod/supervisor_unix.go#L17) | Sentinel |
| W6 | Response headers not filtered (Set-Cookie, CORS leak) | [module_routes.go#L277-L281](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/consoleserver/module_routes.go#L277-L281) | Sentinel |
| W7 | `ModuleCatalog.Close()` double-close panic | [module_catalog.go#L374-L377](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/module_catalog.go#L374-L377) | Logician, Tracer |
| W8 | `Discover()` never populates `Catalog.Epoch` | [discovery.go#L119-L123](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/sdk/discovery.go#L119-L123) | Architect, Cartographer |
| W9 | Synchronous OpenAPI validation blocks catalog commit | [module_catalog.go#L255-L273](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/module_catalog.go#L255-L273) | Performer, Architect |
| W10 | Missing invocation timeout configuration | [module_routes.go#L254](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/consoleserver/module_routes.go#L254) | Performer |
| W11 | Silent error swallowing in module control sender | [agent.go#L294-L303](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agent/agent.go#L294-L303) | Logician |
| W12 | `io.Copy` error silently ignored in handleInvoke | [module_routes.go#L286](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/consoleserver/module_routes.go#L286) | Logician |
| W13 | `IPCServer.Lookup` uses write lock for read-only operation | [ipc.go#L126-L130](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agentmod/ipc.go#L126-L130) | Performer |
| W14 | Invocation proxy lacks structured logging | [module_invoke.go#L139-L179](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agent/module_invoke.go#L139-L179) | Observer |
| W15 | No observability into pending queue overload | [module_stream.go#L79-L85](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/module_stream.go#L79-L85) | Observer |
| W16 | Catalog reassembly has no timeout (permanent block) | [module_catalog.go#L130-L167](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/module_catalog.go#L130-L167) | Observer |
| W17 | Agent invocations not cleaned on tunnel disconnect | [agent.go#L236-L340](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agent/agent.go#L236-L340) | Tracer |
| W18 | `HandleModuleOpen` uses `context.Background()` — no cancellation | [control.go#L145](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agent/control.go#L145) | Tracer |
| W19 | Nil stream from closed `ResponseCh` → potential crash | [module_stream.go#L108-L127](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/module_stream.go#L108-L127) | Tracer |
| W20 | IPC protocol lacks message length bounds | [ipc.go#L211-L220](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agentmod/ipc.go#L211-L220) | Sentinel |
| W21 | Weak randomness in backoff jitter | [registry.go#L402-L405](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agentmod/registry.go#L402-L405) | Sentinel, Performer |

---

## Suggestions (CONSIDER)

| # | Issue | File |
|---|-------|------|
| S1 | Tight coupling: ModuleManager/Invoker store back-references to Agent | [module_invoke.go#L25](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agent/module_invoke.go#L25) |
| S2 | Remove deprecated `AcceptStream` dead code | [module_stream.go#L139-L162](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/module_stream.go#L139-L162) |
| S3 | `CatalogSnapshot.Revision` never transmitted in wire protocol | [module_manager.go#L42](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agent/module_manager.go#L42) |
| S4 | Duplicated limit constants across `sdk` and `moduleproto` | [manifest.go](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/sdk/manifest.go) / [limits.go](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/moduleproto/limits.go) |
| S5 | O(n) asset lookup in CacheAsset — build index in Install() | [store.go#L155-L164](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/moduleui/store.go#L155-L164) |
| S6 | Missing CHANGELOG entry for major feature | [CHANGELOG.md](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/CHANGELOG.md) |
| S7 | ModuleManager sender not cleared on disconnect | [module_manager.go#L56-L60](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agent/module_manager.go#L56-L60) |
| S8 | Add Prometheus/exporter integration for module metrics | [metrics/module.go](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/metrics/module.go) |
| S9 | Health loop doesn't detect/recover from connection failures | [provider.go#L97-L121](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/sdk/provider.go#L97-L121) |
| S10 | Documentation CLI examples use short names instead of full module IDs | [002-agent-modules.md#L70](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/docs/guide/002-agent-modules.md#L70) |

---

## Nits / FYI

| # | Issue | File |
|---|-------|------|
| N1 | `randFloat()` in registry.go uses time-based pseudo-random | [registry.go#L403](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agentmod/registry.go#L403) |
| N2 | `generateCorrelationID` uses crypto/rand for non-security IDs | [module_routes.go#L338-L342](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/consoleserver/module_routes.go#L338-L342) |
| N3 | Windows IPC network type constant is confusing | [ipc_windows.go#L16](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agentmod/ipc_windows.go#L16) |
| N4 | Documentation marker format missing trailing newline | [002-agent-modules.md#L90](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/docs/guide/002-agent-modules.md#L90) |

---

## Summary of Changes

- **Agent Module Registry:** Pluggable HTTP services running as separate processes alongside the agent, with OpenAPI-defined APIs discoverable through the tunnel.
- **Catalog Snapshots:** Agent publishes module catalogs to server via control stream (start/chunk/commit protocol with SHA-256 verification).
- **Reverse-Opened Invocation Streams:** Server sends `module_open` control message, agent opens reverse yamux stream for HTTP exchange — avoids server-to-agent NAT traversal.
- **Secure IPC:** Owner-only Unix sockets (mode 0600) and Windows named pipes for agent↔module process communication.
- **Consumer/Provider SDKs:** Go packages for module authors (`http.Handler`-based) and consumers (HTTP discovery + invocation client).

## Dependency Impact

- **Upstream:** No changes to existing tunnel, shell, or remote desktop paths. Module routes are additive to the console API.
- **Downstream:** The module infrastructure introduces new control message types (`module_open`, `module_close`, `snapshot_*`) that old agents/servers will ignore (backward compatible via capability negotiation `modules=1`).
- **Cross-cutting:** The IPC accept loop stub (C1) and single-connection listener (C2) mean the feature is non-functional end-to-end in its current state. All invocation and catalog flows depend on these working correctly.

---

## Verdict: **Request Changes**

The architecture and protocol design are sound, but the implementation has fundamental gaps that make the feature non-functional:

1. **IPC protocol not implemented** (C1) — the entire module registration path is a stub
2. **Provider SDK serves one request** (C2) — modules die after first invocation
3. **Concurrent invocation corruption** (C3) — shared connection causes data mixing

These three issues alone mean no module can successfully register, serve, or handle concurrent requests. Additionally, security findings (C9 symlink traversal, C10 missing peer verification) must be resolved before production use.

**Recommended approach:** Fix C1-C8 (functional correctness) first, then C9-C10 (security), then C11-C17 (reliability). Address warnings in a follow-up pass. The architecture is solid — this is an implementation-completeness problem, not a design problem.
