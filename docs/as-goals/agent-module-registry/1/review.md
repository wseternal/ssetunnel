# Review — Iteration 1

**Review scope:** main..feat/agent-module-registry (38 files, 6,304 lines)
**Review method:** multi-agent-review (7 specialized agents)
**Pre-Flight:** build=✅ tests=✅ vet=✅

## Findings Summary

| Severity | Count |
|----------|-------|
| Critical | 17 |
| Warning | 21 |
| Suggestion | 10 |
| Nit / FYI | 4 |

## Critical Issues (MUST FIX)

| ID | Issue | Blocking Gates |
|----|-------|----------------|
| C1 | IPC acceptLoop is a no-op stub — entire module invocation path is dead | Module Lifecycle, HTTP Invocation, Go SDK |
| C2 | SDK `singleConnListener` serves exactly one HTTP request, then kills the module | Go SDK, HTTP Invocation |
| C3 | Shared IPC `net.Conn` across concurrent invocations corrupts HTTP responses | HTTP Invocation |
| C4 | `publishSnapshotLocked` holds mutex during blocking network I/O | OpenAPI Discovery, Operations |
| C5 | Nil dereference in `publishCoalescedUpdate` — panic on early registry change | OpenAPI Discovery |
| C6 | Supervisor deadlock: holds mutex during 30s backoff sleep | Module Lifecycle |
| C7 | TOCTOU race on per-module invocation limit | HTTP Invocation, Security |
| C8 | `CancelInvocation`/`DeliverStream` race — send on closed channel → panic | HTTP Invocation |
| C9 | Symlink traversal bypass in package validation — arbitrary code execution | Security Isolation |
| C10 | Missing Linux peer identity verification | Security Isolation |
| C11 | Nil pointer dereference in `CacheAsset` | Console Federation |
| C12 | O(n²) allocation in snapshot commit | OpenAPI Discovery |
| C13 | Goroutine leak in `proxyInvocation` — context never used | HTTP Invocation |
| C14 | Zero test coverage on data plane modules | All gates |
| C15 | Metrics infrastructure defined but never wired | Operations and Compatibility |
| C16 | SDK `Consumer.Cancel()` targets non-existent endpoint | Go SDK |
| C17 | Marker read may consume HTTP request data | HTTP Invocation |

## Warnings (SHOULD FIX)

| ID | Issue | Gate Impact |
|----|-------|-------------|
| W1 | `handleModuleHealth` mutates committed catalog under read lock | OpenAPI Discovery |
| W2 | `OnRegistryChange` captures caller's context in timer closure | Module Lifecycle |
| W3 | Orphaned validation queue/workers never used | OpenAPI Discovery |
| W4 | CORS wildcard on invoke endpoint | Security Isolation |
| W5 | Environment variable inheritance leaks secrets to modules | Security Isolation |
| W6 | Response headers not filtered (Set-Cookie, CORS leak) | Security Isolation |
| W7 | `ModuleCatalog.Close()` double-close panic | OpenAPI Discovery |
| W8 | `Discover()` never populates `Catalog.Epoch` | Go SDK |
| W9 | Synchronous OpenAPI validation blocks catalog commit | OpenAPI Discovery |
| W10 | Missing invocation timeout configuration | HTTP Invocation |
| W11 | Silent error swallowing in module control sender | Module Lifecycle |
| W12 | `io.Copy` error silently ignored in handleInvoke | HTTP Invocation |
| W13 | `IPCServer.Lookup` uses write lock for read-only operation | Module Lifecycle |
| W14 | Invocation proxy lacks structured logging | Operations |
| W15 | No observability into pending queue overload | Operations |
| W16 | Catalog reassembly has no timeout (permanent block) | OpenAPI Discovery |
| W17 | Agent invocations not cleaned on tunnel disconnect | HTTP Invocation |
| W18 | `HandleModuleOpen` uses `context.Background()` — no cancellation | HTTP Invocation |
| W19 | Nil stream from closed `ResponseCh` → potential crash | HTTP Invocation |
| W20 | IPC protocol lacks message length bounds | Security Isolation |
| W21 | Weak randomness in backoff jitter | Module Lifecycle |

## Verdict

**Request Changes** — 17 Critical findings block all functional gates. The architecture is sound but the implementation has fundamental completeness gaps.

Full report: `agent-module-registry-review-findings.md`
