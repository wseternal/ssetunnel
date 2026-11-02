# Plan — Iteration 2

## Goal
Fix all 17 Critical findings from the multi-agent review to make the module invocation data plane functional and secure.

## Tasks (ordered by dependency and priority)

### Task 1: Implement IPC Protocol Handler (C1)
**Files:** `internal/agentmod/ipc.go`
**Acceptance:** acceptLoop handles AUTH, REG, HEALTH messages; modules can register and report health
**Changes:**
- Add `handleConnection(conn)` method
- Parse AUTH frame, verify credential, respond "OK\n"
- Parse REG frame, call Register()
- Parse HEALTH frames in loop until connection closes

### Task 2: Fix Provider SDK Connection Model (C2, C3)
**Files:** `sdk/provider.go`, `internal/agent/module_invoke.go`, `internal/agentmod/ipc.go`
**Acceptance:** Provider can serve multiple concurrent invocations; invocations don't share connections
**Changes:**
- Provider: Replace singleConnListener with persistent listener that accepts multiple connections
- ModuleInvoker: Open fresh IPC connection per invocation (add DialFunc to Registration)
- IPCServer: Store listener endpoint for dialing new connections

### Task 3: Fix Snapshot Publish Lock Granularity (C4)
**Files:** `internal/agent/module_manager.go`
**Acceptance:** Snapshot publish doesn't hold mutex during network I/O
**Changes:**
- Split publishSnapshotLocked into build (under lock) and send (outside lock)
- OnConnect and publishCoalescedUpdate use the new split approach

### Task 4: Fix Nil Guards and Races (C5, C7, C8)
**Files:** `internal/agent/module_manager.go`, `internal/agent/module_invoke.go`, `internal/server/module_stream.go`
**Acceptance:** No panics on early registry change; per-module limits enforced atomically; cancel/deliver race eliminated
**Changes:**
- C5: Guard nil snapshot in publishCoalescedUpdate
- C7: Reserve per-module slot before releasing lock for stream open
- C8: Use `done` channel per pending invocation; don't close ResponseCh

### Task 5: Fix Supervisor Backoff Deadlock (C6)
**Files:** `internal/agentmod/registry.go`
**Acceptance:** Supervisor Stop() doesn't block for 30s during backoff
**Changes:**
- Release lock before time.After sleep in monitorProcess
- Re-acquire and re-validate after waking

### Task 6: Fix Security Issues (C9, C10)
**Files:** `internal/agentmod/package.go`, `internal/agentmod/ipc_unix.go`
**Acceptance:** Symlink traversal blocked; Linux SO_PEERCRED implemented
**Changes:**
- C9: Use EvalSymlinks and Lstat in isSubPath and ValidateExecutable
- C10: Implement SO_PEERCRED check on Linux (keep macOS/BSD stub)

### Task 7: Fix Reliability Issues (C11, C12, C13, C17)
**Files:** `internal/moduleui/store.go`, `internal/server/module_catalog.go`, `internal/agent/module_invoke.go`, `internal/server/server.go`
**Acceptance:** No panics on new asset caching; O(n) allocation; context deadlines enforced; marker read preserves HTTP data
**Changes:**
- C11: Guard nil oldAsset in CacheAsset
- C12: Pre-allocate reassembly buffer
- C13: Use context deadline in proxyInvocation
- C17: Use bufio.Reader for marker line read

### Task 8: Wire Metrics and Remove Dead Code (C15, C16)
**Files:** `internal/agent/module_invoke.go`, `internal/agent/module_manager.go`, `internal/server/module_catalog.go`, `sdk/discovery.go`
**Acceptance:** ModuleMetrics called on invocation and catalog paths; SDK Cancel removed or endpoint added
**Changes:**
- C15: Wire RecordInvocation, RecordBytes, RecordError into paths
- C16: Remove Cancel() from Consumer SDK (endpoint not implemented)

## Out of Scope (deferred to iteration 3)
- C14: Table-driven tests (add after functional fixes)
- W1-W21: Warning-level fixes
- Tasks 14, 16-18: E2E tests and frontend federation
