# Verified Review Findings

- **Source Audit:** Inline cross-model verification context review (`201eaf7..HEAD`, readiness score 2/100)
- **Adjudication Summary:** `11 Total Claims → 10 Verified, 1 Partially Rejected (1 false positive sub-claim in S2)`

---

## 🛑 Critical Findings (Verified)

### C1. Cross-Module Data Access and Overwrite via Unverified `module_id`
- **Location:** [`internal/agentmod/ipc.go:474-531`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agentmod/ipc.go#L474-L531) → [`internal/agent/agent.go:221-231`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agent/agent.go#L221-L231) → [`internal/server/session.go:339-349`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/session.go#L339-L349) → [`internal/server/datastore.go`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/datastore.go)
- **The Issue:** Any module connecting to the IPC socket can read, overwrite, or delete records belonging to any other module in `module_records`. The `module_id` in data store requests is taken verbatim from the calling module's JSON payload. No layer validates that `module_id` matches the authenticated caller's identity.
- **Audit Confirmation:** Traced the full pipeline:
  1. `ipc.go:474` — `handleDataStoreConnection` accepts DATASTORE commands after AUTH without establishing module identity (unlike REG flow which binds via `REG <moduleID>` handshake).
  2. `agent.go:222-224` — Agent callback unmarshals `req.ModuleID` from JSON and passes directly to `DataStoreRequest(req)` without override.
  3. `session.go:341` — Server dispatches to `s.dataStore.HandleRequest(msg)` with no module_id validation.
  4. `datastore.go:86-91` — Server SQL queries use `req.ModuleID` directly in INSERT/SELECT/DELETE.
- **Recommendation:** Require DATASTORE IPC connections to declare module identity (e.g., `DATASTORE_IDENT <module_id>` handshake step after AUTH), and have the IPC handler override `req.ModuleID` with the authenticated identity before forwarding. Add `sdk.ModuleIDRegexp` validation on the server side.

### C2. Data Race and Concurrent Map Panic in `Registry.Reload()`
- **Location:** [`internal/agentmod/registry.go:232-246`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agentmod/registry.go#L232-L246)
- **The Issue:** `Reload()` clears `r.packages` and `r.desired` under `r.mu.Lock()`, releases the lock, then calls `loadPackages()`/`loadDesiredState()` which write to those maps **without the lock**. Concurrent `RLock()` readers (`EnabledModules()`, `Package()`) can cause a Go runtime panic (`fatal error: concurrent map read and map write`), or observe empty maps causing `Supervisor.Reconcile()` to stop all running modules.
- **Audit Confirmation:** Verified the race path:
  1. `registry.go:233-236` — Lock → clear maps → Unlock
  2. `registry.go:262` — `json.Unmarshal(data, &r.packages)` writes without lock
  3. `registry.go:312` — `json.Unmarshal(data, &r.desired)` writes without lock
  4. `registry.go:287-296` — `pruneOrphanDesiredState()` iterates both maps without lock
  5. Concurrent readers use `r.mu.RLock()` (lines 138, 145, 153, 166) — race against unprotected writes
- **Recommendation:** Load into temporary variables outside the lock, then atomically swap under a single write lock.

### C3. Unframed IPC Responses and 64KB Buffer Truncate/Desynchronize `DATASTORE` Streams
- **Location:** [`internal/agentmod/ipc.go:508-514`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agentmod/ipc.go#L508-L514) + [`sdk/datastore.go:226-239`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/sdk/datastore.go#L226-L239)
- **The Issue:** Request path uses proper length-prefix framing (`DATASTORE <len>\n<json>`), but responses are written as raw JSON with no length prefix. SDK reads with a single `conn.Read()` into a 64KB buffer. Responses >64KB truncate mid-JSON; partial socket reads corrupt subsequent operations on the persistent connection.
- **Audit Confirmation:**
  1. `ipc.go:508-514` — Response written via `conn.Write(respJSON)` with no length prefix.
  2. `sdk/datastore.go:227-228` — `n, err := ds.conn.Read(respBuf)` into fixed 64KB buffer; single Read call.
  3. `sdk/datastore.go:182-183` — `connect()` also uses `conn.Read(buf)` instead of `io.ReadFull` for `"OK\n"` auth reply (3 bytes, partial read possible).
  4. `server/datastore.go:158-160` — list() permits LIMIT up to 1000 JSONB records — easily exceeds 64KB.
- **Recommendation:** Add length-prefix framing to the response path (`RESP <len>\n<json>`). Use `bufio.Reader` + `io.ReadFull` on the SDK side. Use `io.ReadFull` for auth reply too.

---

## ⚠️ Warning Findings (Verified)

### W1. Unauthenticated Extension Long-Poll Server Exposed on `0.0.0.0:9090`
- **Location:** [`cmd/ai-automation-proxy/main.go`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/cmd/ai-automation-proxy/main.go) + [`internal/aiproxy/longpoll.go`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/aiproxy/longpoll.go)
- **The Issue:** Extension HTTP server defaults to `:9090` (all interfaces) with no authentication on `/poll` or `/result`. Any local user or LAN peer can poll pending jobs (exfiltrating prompts) and inject forged results.
- **Audit Confirmation:** `main.go` — `flag.String("listen", ":9090", ...)`. `longpoll.go:62-98` — `handlePoll` and `handleResult` have no auth middleware.
- **Recommendation:** Default to `127.0.0.1:9090` and add bearer token auth on extension endpoints.

### W2. Job Loss on Extension Disconnect and Unbounded `JobManager.jobs` Growth
- **Location:** [`internal/aiproxy/longpoll.go:80-98`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/aiproxy/longpoll.go#L80-L98) + [`internal/aiproxy/jobs.go`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/aiproxy/jobs.go)
- **The Issue:** (a) Job dequeued from `PendingChan()` and marked `Running` before `writeJSON` succeeds — if write fails or client disconnects, job is stranded until 5-min timeout. (b) `m.jobs` never pruned — terminal jobs accumulate forever.
- **Audit Confirmation:** `longpoll.go:81-91` — dequeue → `MarkRunning` → `writeJSON` with no rollback. `jobs.go:109` — Submit adds; no function deletes from `m.jobs`.
- **Recommendation:** Add `Requeue()` method for write failures; add TTL-based reaper for terminal jobs.

### W3. Unbounded Request Bodies and Uncapped `wait_timeout` DoS Vectors
- **Location:** [`internal/aiproxy/handler.go:58`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/aiproxy/handler.go#L58) + [`internal/aiproxy/handler.go:98-103`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/aiproxy/handler.go#L98-L103)
- **The Issue:** `handleSubmit`/`handleResult` decode JSON without `http.MaxBytesReader`. `handleGet` accepts unbounded `wait_timeout` (unlike `handlePoll` which caps at 5 min).
- **Audit Confirmation:** `handler.go:58` — `json.NewDecoder(r.Body).Decode(&req)` with no body limit. `handler.go:100` — `time.ParseDuration(v)` with no upper bound.
- **Recommendation:** Apply `http.MaxBytesReader(w, r.Body, 4<<20)`. Cap `waitTimeout` at 5 minutes.

### W4. Missing `rows.Err()`, `pgx.ErrNoRows` Masking, and Nil JSON Slice in Server `DataStore`
- **Location:** [`internal/server/datastore.go:111-192`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/datastore.go#L111-L192)
- **The Issue:** (a) `list()` doesn't check `rows.Err()` after iteration. (b) `get()` treats all errors as "record not found". (c) Empty list returns nil slice → JSON `null`.
- **Audit Confirmation:** `datastore.go:187` — no `rows.Err()` after loop. `datastore.go:126-130` — unconditional `"record not found"`. `datastore.go:176` — `var records []moduleproto.DataStoreRecord` (nil-initialized).
- **Recommendation:** Add `rows.Err()` check, distinguish `pgx.ErrNoRows`, initialize with `make([]..., 0)`.

---

## 💡 Suggestion Findings (Verified)

### S1. Composite Index `(module_id, created_at DESC)` on `module_records`
- **Location:** [`migrations/20260724000009_add_module_records.sql:16`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/migrations/20260724000009_add_module_records.sql#L16)
- **The Issue:** Only `("module_id")` indexed; list query uses `ORDER BY created_at DESC`.
- **Audit Confirmation:** Migration creates `module_records_module_id_idx` on `("module_id")` only.
- **Recommendation:** Replace with `CREATE INDEX ... ON "public"."module_records" ("module_id", "created_at" DESC)`.

### S2. Guard `StderrPipe()` Error and Add Job Lifecycle Logging
- **Location:** [`internal/agentmod/supervisor_unix.go:32`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/agentmod/supervisor_unix.go#L32) + [`internal/aiproxy/jobs.go`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/aiproxy/jobs.go)
- **The Issue:** (a) `stderr, _ := cmd.StderrPipe()` discards error — if pipe creation fails, `bufio.NewScanner(nil)` panics. (b) `JobManager` has zero log statements across all state transitions.
- **Audit Confirmation:**
  - `supervisor_unix.go:32` — `stderr, _ := cmd.StderrPipe()` with `_` ignoring error. Verified: if this fails, `stderr` is nil and `bufio.NewScanner(nil)` panics on first `Scan()` call.
  - `jobs.go` — grep for `log\.` returns zero matches.
  - **Note:** The review also claimed `ReloadModules()` discards errors — this sub-claim is **rejected**. `ReloadModules()` at `agent.go:249-253` properly wraps and returns errors, and the caller at `reload_unix.go:24` checks and logs them.
- **Recommendation:** Check `err` from `StderrPipe()` before starting scanner goroutine. Add `log.Printf` at each job state transition.

### S3. Synchronize `Server.SetDataStore` and Wire Auto-Reconnect in `sdk.DataStore`
- **Location:** [`internal/server/server.go:86-88`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/server/server.go#L86-L88) + [`sdk/datastore.go:247-256`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/sdk/datastore.go#L247-L256)
- **The Issue:** (a) `Server.dataStore` written in `SetDataStore` and read in `attach()` goroutine without synchronization. (b) `sdk.DataStore.Reconnect()` is dead code — never called on connection errors.
- **Audit Confirmation:** `server.go:86-88` — plain assignment `s.dataStore = ds`, no atomic.Pointer. `server.go:164` — `sess.SetDataStore(s.dataStore)` reads from goroutine. `sdk/datastore.go:248` — grep confirms 0 callers of `Reconnect()`.
- **Recommendation:** Use `atomic.Pointer[DataStore]` for the field. Either wire `Reconnect()` into retry logic or remove the dead code.

---

## ℹ️ Nit Findings (Verified)

### N1. `SendReload` Silently Ignores Missing or Unreachable Agent Process
- **Location:** [`internal/command/service.go:401-420`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/command/service.go#L401-L420) + [`internal/clicommand/module.go:44-47`](file:///Users/jiangzhaohua/codes/wseternal/ssetunnel/internal/clicommand/module.go#L44-L47)
- **The Issue:** `SendReload` returns nil on all error paths. Operators have no feedback when the agent isn't running.
- **Audit Confirmation:** `service.go:407,411,416` — all error paths return `nil`.
- **Recommendation:** Return an error or print a brief stderr notice when agent is not running.

---

## 🗑️ Rejected Claims Summary (For Transparency)

- **[False Positive] `ReloadModules()` discards errors:** Rejected. `agent.go:249-253` properly wraps errors from `Reload()` and `Reconcile()` with `fmt.Errorf("...: %w", err)` and returns them. The caller at `reload_unix.go:24` checks the error and logs it via `log.Printf("agent: module reload: %v", err)`. Errors are neither discarded nor silently dropped.
