# Gap Summary — Iteration 1

## Failed Gates
- **Module Lifecycle:** C1 IPC acceptLoop stub, C6 supervisor lock-during-sleep → routed to Engineer
- **OpenAPI Discovery:** C4 mutex during network I/O, C5 nil dereference, C12 O(n²) allocation → routed to Engineer
- **HTTP Invocation Data Plane:** C3 shared conn corruption, C7 TOCTOU race, C8 cancel/deliver race, C13 goroutine leak, C17 marker read → routed to Engineer
- **Unified Go SDK:** C2 singleConnListener one-request death, C16 Cancel missing endpoint → routed to Engineer
- **Security and Isolation:** C9 symlink bypass, C10 missing peer verification → routed to Engineer
- **Operations and Compatibility:** C15 metrics never wired, W14 no structured logging → routed to Engineer

## Unresolved Findings

### Critical (17)
C1-C17 as listed in review.md

### High-Priority Warnings (21)
W1-W21 as listed in review.md

## Next Iteration Focus

### Priority 1: Functional Correctness (C1-C8)
1. **C1** — Implement IPC acceptLoop with full protocol handler (AUTH, REG, HEALTH)
2. **C2** — Replace singleConnListener with persistent connection pool
3. **C3** — Open fresh IPC connection per invocation (or per-module mutex)
4. **C4** — Build snapshot messages under lock, send outside lock
5. **C5** — Guard nil snapshot in publishCoalescedUpdate
6. **C6** — Release supervisor lock before backoff sleep
7. **C7** — Reserve per-module slot atomically before stream open
8. **C8** — Use done channel per pending invocation, don't close ResponseCh

### Priority 2: Security (C9-C10)
9. **C9** — Add EvalSymlinks and Lstat checks in package validation
10. **C10** — Implement SO_PEERCRED on Linux

### Priority 3: Reliability (C11-C17)
11. **C11** — Guard nil oldAsset in CacheAsset
12. **C12** — Pre-allocate snapshot reassembly buffer
13. **C13** — Use context deadline in proxyInvocation
14. **C14** — Add table-driven tests for data plane
15. **C15** — Wire ModuleMetrics into invocation and catalog paths
16. **C16** — Implement cancel endpoint or remove from SDK
17. **C17** — Use bufio.Reader for marker line read

### Priority 4: High-Priority Warnings
- W4 CORS wildcard, W5 env inheritance, W6 response header filtering
- W7 double-close panic, W17 cleanup on disconnect
