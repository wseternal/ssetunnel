# PR #98 — Request Changes

**Readiness Score: 14/100** (Fundamental issues — significant rework required)
**Pre-Flight:** build=✅ tests=❌ (atlas.sum checksum mismatch, passes after fix) vet=✅
**Agents dispatched:** Architect, Logician, Sentinel, Performer, Cartographer, Observer, Tracer
**Audit Adjudication:** 7-agent review (13 claims) + cross-model verification (11 claims) → 14 unique verified findings, 1 false positive rejected

## Findings Summary
| Severity | Count | Group Risk |
|----------|-------|------------|
| Critical | 4 | High — data isolation, broken test suite, map race panic, IPC stream corruption |
| Warning | 5 | High — unauthenticated endpoint, stranded jobs, DoS vectors, missing DB error checks |
| Suggestion | 4 | Medium — index optimization, API consistency, StderrPipe guard, sync |
| Nit / FYI | 1 | Low — operator feedback |

## Critical Issues (MUST FIX)

### C1. Module data isolation violation — any module can read/write/delete another module's records
[sdk/datastore.go#L95](sdk/datastore.go) → [internal/agentmod/ipc.go#L474](internal/agentmod/ipc.go) → [internal/agent/agent.go#L221](internal/agent/agent.go) → [internal/server/datastore.go#L55](internal/server/datastore.go) — *Flagged by: Architect, Sentinel*

**Risk:** High — any module sharing the IPC credential can access any other module's persisted records by simply setting a different `module_id` in the request payload.

**Problem:** The `module_id` in data store requests is taken verbatim from the calling module's JSON payload. No layer validates that `module_id` matches the authenticated caller's registered identity. A compromised or malicious module can set `module_id` to `"com.wseternal.other-module"` and freely read, overwrite, or delete records belonging to other modules.

**Fix:** Require DATASTORE IPC connections to carry the caller's module ID. Either:
- Add a `DATASTORE_IDENT <module_id>` handshake step after AUTH, and have the IPC handler override `req.ModuleID` with the authenticated identity before forwarding.
- Or reject anonymous DATASTORE connections and require modules to use REG-based connections where the module ID is established at registration.

### C2. Stale `atlas.sum` checksums break all database tests
[migrations/atlas.sum](migrations/atlas.sum) — *Flagged by: Cartographer*

**Risk:** High — `go test ./...` fails for every test that uses TestContainers (auth, consoleapi, server). CI and production deployments are blocked.

**Problem:** The committed `atlas.sum` contains checksums that don't match the actual migration files. Running `atlas migrate hash` produces different hashes.

**Fix:** Regenerate with `atlas migrate hash` and commit the corrected file.

### C3. `Registry.Reload()` data race — concurrent map panic on SIGHUP
[internal/agentmod/registry.go#L232-L246](internal/agentmod/registry.go) — *Flagged by: Architect, Logician, Tracer; Upgraded by: Cross-model review*

**Risk:** Critical — Go runtime panic (`fatal error: concurrent map read and map write`) or empty-map visibility window causing `Supervisor.Reconcile()` to stop all running modules.

**Problem:** `Reload()` clears `r.packages` and `r.desired` under lock, releases the lock, then calls `loadPackages()`/`loadDesiredState()` which write to those maps **without the lock**. Concurrent `RLock()` readers (`EnabledModules()`, `Package()`) race against unprotected writes.

**Fix:** Load into temporary variables outside the lock, then atomically swap:
```go
func (r *Registry) Reload() error {
    pkgs, err := r.readPackagesFromDisk()
    if err != nil { return fmt.Errorf("reload packages: %w", err) }
    desired, err := r.readDesiredStateFromDisk()
    if err != nil { return fmt.Errorf("reload desired state: %w", err) }
    for id := range desired { if _, ok := pkgs[id]; !ok { delete(desired, id) } }
    r.mu.Lock()
    r.packages = pkgs
    r.desired = desired
    r.mu.Unlock()
    return nil
}
```

### C4. Unframed IPC responses + 64KB buffer truncate/corrupt `DATASTORE` streams
[sdk/datastore.go#L226-L239](sdk/datastore.go) + [internal/agentmod/ipc.go#L508-L514](internal/agentmod/ipc.go) — *Flagged by: Architect, Logician, Performer, Cartographer, Observer; Upgraded by: Cross-model review*

**Risk:** Critical — any `list()` response >64KB (valid with LIMIT 1000 JSONB) truncates mid-JSON, fails unmarshaling, and leaves unread bytes corrupting all subsequent operations on the persistent IPC connection.

**Problem:** Request path uses `DATASTORE <len>\n<json>` framing, but responses are raw JSON with no length prefix. SDK reads with single `conn.Read()` into 64KB buffer. Additionally, `connect()` uses `conn.Read(buf)` instead of `io.ReadFull` for auth reply.

**Fix:** Frame responses with `RESP <len>\n<json>` header. Use `bufio.Reader` + `io.ReadFull` on SDK side. Use `io.ReadFull` for auth reply.

## Warnings (SHOULD FIX)

### W1. Unauthenticated extension server on `0.0.0.0:9090`
[cmd/ai-automation-proxy/main.go](cmd/ai-automation-proxy/main.go) + [internal/aiproxy/longpoll.go](internal/aiproxy/longpoll.go) — *New from: Cross-model review*

**Problem:** Extension HTTP server defaults to `:9090` (all interfaces) with no authentication. Any local user or LAN peer can poll pending jobs and inject forged results.

**Fix:** Default to `127.0.0.1:9090`. Add bearer token auth with `crypto/subtle.ConstantTimeCompare`.

### W2. Job loss on extension disconnect + unbounded `JobManager.jobs` growth
[internal/aiproxy/longpoll.go#L80-L98](internal/aiproxy/longpoll.go) + [internal/aiproxy/jobs.go](internal/aiproxy/jobs.go) — *Flagged by: Tracer, Performer*

**Problem:** (a) Job dequeued from `PendingChan()` and marked `Running` before `writeJSON` succeeds — stranded until 5-min timeout if write fails. (b) `m.jobs` never pruned — terminal jobs accumulate forever (OOM under sustained load).

**Fix:** Add `Requeue()` method. Schedule TTL eviction when jobs enter terminal state.

### W3. Unbounded request bodies and uncapped `wait_timeout` DoS vectors
[internal/aiproxy/handler.go#L57](internal/aiproxy/handler.go) + [internal/aiproxy/handler.go#L98-L103](internal/aiproxy/handler.go) — *Flagged by: Sentinel*

**Problem:** `handleSubmit`/`handleResult` decode JSON without `http.MaxBytesReader`. `handleGet` accepts unbounded `wait_timeout` (unlike `handlePoll` which caps at 5 min).

**Fix:** Apply `http.MaxBytesReader(w, r.Body, 4<<20)`. Cap `waitTimeout` at 5 minutes.

### W4. Missing `rows.Err()`, `pgx.ErrNoRows` masking, and nil JSON slice in server `DataStore`
[internal/server/datastore.go#L111-L192](internal/server/datastore.go) — *Flagged by: Performer, Cartographer, Observer*

**Problem:** (a) `list()` doesn't check `rows.Err()` after iteration. (b) `get()` treats all errors as "record not found" without distinguishing `pgx.ErrNoRows`. (c) Empty list returns nil slice → JSON `null`.

**Fix:** Add `rows.Err()` check. Use `errors.Is(err, pgx.ErrNoRows)`. Initialize with `make([]..., 0)`.

### W5. `StderrPipe()` error ignored — nil pipe panics in scanner goroutine
[internal/agentmod/supervisor_unix.go#L32](internal/agentmod/supervisor_unix.go) — *New from: Cross-model review*

**Problem:** `stderr, _ := cmd.StderrPipe()` discards error. If pipe creation fails (e.g., `EMFILE`), `bufio.NewScanner(nil)` panics on first `Scan()`.

**Fix:** Check `err` before starting the scanner goroutine; fall back to `cmd.Stderr = nil` on failure.

## Suggestions (CONSIDER)

### S1. Missing composite index for `ORDER BY created_at DESC`
[migrations/20260724000009_add_module_records.sql#L16](migrations/20260724000009_add_module_records.sql)

The list query uses `WHERE module_id = $1 ORDER BY created_at DESC` but only has a single-column index on `(module_id)`. Replace with `CREATE INDEX ... ("module_id", "created_at" DESC)`.

### S2. List returns JSON `null` instead of `[]` for empty results
[internal/server/datastore.go#L176](internal/server/datastore.go)

`var records []moduleproto.DataStoreRecord` is nil-initialized and marshals to JSON `null`. Initialize as `make([]moduleproto.DataStoreRecord, 0)` for consistent `[]`.

### S3. Synchronize `Server.SetDataStore` and wire auto-reconnect in `sdk.DataStore`
[internal/server/server.go#L86-L88](internal/server/server.go) + [sdk/datastore.go#L247-L256](sdk/datastore.go) — *New from: Cross-model review*

`Server.dataStore` written/read without synchronization. `sdk.DataStore.Reconnect()` is dead code. Use `atomic.Pointer[DataStore]` and either wire `Reconnect()` into retry logic or remove it.

### S4. Zero logging in job lifecycle
[internal/aiproxy/jobs.go](internal/aiproxy/jobs.go)

No log statements at any state transition. Add `log.Printf` at each transition with job ID and status.

## Nits / FYI

### N1. `SendReload` silently ignores missing agent process
[internal/command/service.go](internal/command/service.go) + [internal/clicommand/module.go](internal/clicommand/module.go)

`SendReload` returns nil on all error paths. Consider returning an error or printing a notice when the agent isn't running.

## Summary of Changes
- **AI automation proxy module** (`internal/aiproxy/`, `cmd/ai-automation-proxy/`): New module bridging external clients to a WXT browser extension via HTTP long-polling, with a `JobManager` handling job lifecycle and two HTTP surfaces.
- **Generic data store framework**: End-to-end CRUD pipeline (SDK → IPC → agent → control tunnel → server → PostgreSQL `module_records` table) allowing modules to persist structured records without direct DB access.
- **Module registry disk persistence & hot-reload**: Package metadata persisted to `packages.json`; agent supports SIGHUP-triggered reload for CLI module commands without agent restart.
- **Module ID regex consolidation**: Exported `sdk.ModuleIDRegexp` as single source of truth with hyphen support in segments.
- **Module stderr forwarding**: Supervisor captures module stderr and forwards to agent log with module prefix.

## Dependency Impact
- **Upstream:** `internal/server/session.go` gains `MsgDataStoreReq` dispatch; `internal/agent/control.go` gains `MsgDataStoreResp` dispatch; `internal/mux/control.go` adds two new message types.
- **Downstream:** All packages importing `sdk` see the new `DataStore` type and exported `ModuleIDRegexp`. Module packages gain JSON tags on `Package`, `UIDescriptor`, `AssetEntry` — no breaking change since no prior on-disk format existed.
- **Schema:** New `module_records` table with `atlas.sum` drift (Critical C2).
