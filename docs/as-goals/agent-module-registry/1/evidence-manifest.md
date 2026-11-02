# Evidence Manifest — Iteration 1

## Gate Status

| Gate | Status | Evidence | Owner |
|------|--------|----------|-------|
| Dynamic Module Lifecycle | ❌ Fail | Missing: end-to-end lifecycle test; C1 IPC stub blocks registration; C6 supervisor deadlock blocks stop | Engineer |
| OpenAPI Discovery | ❌ Fail | Missing: discovery integration test; C4 mutex-during-IO blocks snapshot; C5 nil panic; C12 O(n²) allocation | Engineer |
| HTTP Invocation Data Plane | ❌ Fail | Missing: invocation test; C1 blocks all invocations; C2 one-request kill; C3 data corruption; C7 TOCTOU; C8 race; C13 goroutine leak; C17 marker read | Engineer |
| Unified Go SDK | ❌ Fail | Missing: example source; C2 provider dies after one request; C16 Cancel targets missing endpoint | Engineer |
| Console Module Federation | ⏸️ Deferred | Tasks 16-18 deferred to later iteration (frontend work) | Frontend Engineer |
| Security and Isolation | ❌ Fail | Missing: security test results; C9 symlink bypass; C10 missing peer verification | Security Engineer |
| Operations and Compatibility | ❌ Fail | Missing: metrics wired; C15 metrics never called; W14 no structured logging | Release Engineer |

## Return Shipments (Failed Gates)

### Gate: Dynamic Module Lifecycle
**Defect:** IPC acceptLoop is a stub (C1), supervisor holds lock during sleep (C6)
**Root Cause:** Implementation incomplete — accept loop and supervisor lifecycle were scaffolded but not fully implemented
**Routed To:** Engineer
**Priority:** Critical

### Gate: OpenAPI Discovery
**Defect:** Snapshot publish holds mutex during network I/O (C4), nil dereference on early registry change (C5), O(n²) chunk assembly (C12)
**Root Cause:** Lock granularity not considered for network-bound operations; missing nil guards
**Routed To:** Engineer
**Priority:** Critical

### Gate: HTTP Invocation Data Plane
**Defect:** IPC connection sharing corrupts concurrent requests (C3), TOCTOU race on limits (C7), cancel/deliver race (C8), context unused (C13), marker read consumes request data (C17)
**Root Cause:** Concurrency model for IPC and invocations not properly designed
**Routed To:** Engineer
**Priority:** Critical

### Gate: Unified Go SDK
**Defect:** Provider SDK serves one request then dies (C2), Consumer.Cancel targets non-existent endpoint (C16)
**Root Cause:** singleConnListener was a placeholder; cancel endpoint was never implemented
**Routed To:** Engineer
**Priority:** Critical

### Gate: Security and Isolation
**Defect:** Symlink traversal bypass (C9), missing Linux peer identity verification (C10)
**Root Cause:** Package validation uses filepath.Rel which doesn't resolve symlinks; SO_PEERCRED not implemented
**Routed To:** Engineer (Security Engineer to verify fix)
**Priority:** Critical

### Gate: Operations and Compatibility
**Defect:** ModuleMetrics defined but never wired (C15), no structured logging (W14)
**Root Cause:** Metrics instrumentation was deferred during initial implementation
**Routed To:** Engineer
**Priority:** Critical

## Code Quality Findings
- Critical: 17
- Warning: 21
- Suggestion: 10
- Nit: 4

## Commits Reviewed
- `13c4d02`: feat(modules): add public SDK contract and wire protocol
- `aee1938`: feat(modules): add package validation and secure IPC
- `3dc5cd8`: feat(modules): add registry and process supervisor
- `7bc21b0`: feat(modules): add local CLI lifecycle commands
- `77ea3ad`: feat(modules): add provider SDK
- `1970e93`: feat(modules): add agent catalog snapshots and server catalogs
- `14ed087`: feat(modules): add invocation streams and discovery APIs
- `fbd74e5`: feat(sdk): implement consumer SDK
- `0bbfd30`: feat(modules): add trusted UI stores and module metrics
- `1678cf8`: docs: add agent modules guide
