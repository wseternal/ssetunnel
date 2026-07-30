# Implementation Plan: Auto-Tuning Metrics & Statistics Console

**Spec**: `docs/specs/auto_tuning_metrics.md`
**Status**: Draft

## Overview

Seven phases, strictly ordered. Each phase is independently testable and shippable. Total estimated scope: ~1200 lines of new Go, ~200 lines of frontend TSX, 1 new Go dependency (`badger/v4`), 1 new npm dependency (`recharts`).

---

## Phase 1: Foundation — Types, Collector, BadgerDB Store

**Dependencies**: None (greenfield package)

### Files to create

| File | Purpose |
|------|---------|
| `internal/metrics/doc.go` | Package doc comment |
| `internal/metrics/types.go` | `MetricSample`, `TuningDecision`, `TransportParams`, `MetricSnapshot`, `AgentMetrics` |
| `internal/metrics/collector.go` | `MetricsCollector`: thread-safe in-memory accumulator with 10s flush |
| `internal/metrics/collector_test.go` | Unit tests for recording, rolling window, sample generation |
| `internal/metrics/store.go` | `Store`: BadgerDB persistence for samples, decisions, window state |
| `internal/metrics/store_test.go` | Unit tests with `t.TempDir()` BadgerDB instances |

### Dependency change

- Add `github.com/dgraph-io/badger/v4` to `go.mod`

### Key implementation details

**types.go** — Use the exact structs from the spec (`MetricSample`, `TuningDecision`, `TransportParams`). Add:
- `MetricSnapshot` struct: aggregated view of the 5-min rolling window (throughput p50/p95, error rate, active conns)
- `AgentMetrics` struct: per-agent current state for the console API (latest snapshot + current `TransportParams` + last decision)

**collector.go** — `MetricsCollector` struct:
- Fields: `mu sync.Mutex`, `windows map[string]*rollingWindow`, `store *Store`, `flushInterval time.Duration`, `retention time.Duration`
- Rolling window: per-agent ring buffer of raw events (POST sizes, RTTs, errors). 5-minute sliding window. Compute percentiles on demand (sort-based, not histogram — agent count is small).
- Recording methods (all nil-safe on `*MetricsCollector` receiver for no-op when metrics disabled):
  - `RecordAgentPost(agentID string, bytes int, rtt time.Duration)` — called from `handleUp`
  - `RecordAgentSSEBytes(agentID string, bytes int)` — called from `handleEvents` SSE frame write
  - `RecordConnectBytes(agentID string, up, down int)` — called from `handleConnect`/`handleConnectUp`
  - `RecordSessionStart(agentID string)` / `RecordSessionEnd(agentID string)` — called from `handleEvents`
  - `RecordError(agentID string, kind string)` — POST failures, session deaths
- Flush goroutine (every 10s): aggregate window → one `MetricSample` per agent → `store.WriteSamples()`. Also prune old entries.
- Query methods for the API: `Overview()`, `AgentMetrics(agentID string)`, `AgentTimeSeries(agentID, from, to)`, `AllAgentMetrics()`

**store.go** — `Store` struct wrapping `*badger.DB`:
- `OpenStore(dir string) (*Store, error)` — `badger.Open(badger.DefaultOptions(dir))`
- Key scheme (from spec):
  - `m:<agentID>:<unix_nano_padded>` → JSON `MetricSample`
  - `t:<agentID>:<unix_nano_padded>` → JSON `TuningDecision`
  - `w:<agentID>` → JSON rolling window state
- `WriteSamples([]MetricSample) error` — batch write via `db.Update` + `txn.Set`
- `QuerySamples(agentID string, from, to time.Time) ([]MetricSample, error)` — prefix scan `m:<agentID>:` with time range
- `WriteDecision(TuningDecision) error`
- `QueryDecisions(agentID string, limit int) ([]TuningDecision, error)` — reverse prefix scan
- `PruneOlderThan(retention time.Duration) error` — scan all `m:` and `t:` keys, delete expired
- `WriteWindow(agentID string, state []byte) error` / `ReadWindow(agentID string) ([]byte, error)`
- `Close() error`
- Time padding: use `fmt.Sprintf("%020d", unixNano)` for lexicographic = chronological ordering

---

## Phase 2: Auto-Tuner

**Dependencies**: Phase 1 (uses `Collector` and `Store`)

### Files to create

| File | Purpose |
|------|---------|
| `internal/metrics/tuner.go` | `AutoTuner`: periodic evaluation loop, decision logic, SSE push |
| `internal/metrics/tuner_test.go` | Unit tests with synthetic metric inputs for each heuristic branch |

### Key implementation details

**tuner.go** — `AutoTuner` struct:
- Fields: `collector *Collector`, `store *Store`, `pushFn func(agentID string, params TransportParams) error`, `interval time.Duration` (default 30s)
- `NewAutoTuner(collector, store, pushFn, interval) *AutoTuner`
- `Run(ctx context.Context)` — `time.Ticker` loop: iterate active agents from collector, call `Evaluate` for each
- `Evaluate(agentID string) (*TuningDecision, error)` — decision logic:

**Decision logic** (per agent, per tick):
1. Read 5-min rolling window from collector
2. **Stability guard**: skip if < 2 minutes since last decision for this agent (track `lastDecision map[string]time.Time`)
3. **Throughput saturation**: if throughput within 80% of batch ceiling → increase batch (max 1 MiB). If < 30% of ceiling for 2+ consecutive evaluations → decrease (floor 4 KiB). Track consecutive undersaturation count per agent.
4. **Latency-driven concurrency**: if p95 RTT > 500ms and throughput not scaling → increase concurrency (max 4). If error rate > 5% and concurrency > 1 → decrease (backpressure relief).
5. **Compression**: if throughput_up < 100 KB/s and low error rate → enable gzip. If throughput_up > 1 MB/s → disable gzip.
6. **One change per evaluation**: pick the single most impactful axis; do not change multiple parameters simultaneously.
7. On decision: persist via `store.WriteDecision()`, push via `pushFn(agentID, newParams)`

**Testing strategy** — each heuristic branch gets a dedicated test case:
- `TestTuner_ThroughputSaturation_IncreaseBatch`
- `TestTuner_ThroughputUndersaturation_DecreaseBatch`
- `TestTuner_LatencyConcurrency_Increase`
- `TestTuner_ErrorRateConcurrency_Decrease`
- `TestTuner_Compression_LowBandwidth`
- `TestTuner_Compression_HighBandwidth`
- `TestTuner_StabilityGuard_MinInterval`
- `TestTuner_StabilityGuard_OneParamPerEval`

---

## Phase 3: Server Integration — Wire Metrics into Handlers

**Dependencies**: Phase 1, Phase 2

### Files to modify

| File | Change |
|------|--------|
| `internal/server/handlers.go` | Add `metrics *metrics.Collector` field; call recording methods in handlers |
| `internal/server/server.go` | Add `SetMetricsCollector(collector)` method; wire tuner pushFn |
| `internal/server/session.go` | Expose RTT tracking for POST latency (add `RecordRTT` or use timestamps) |

### Key implementation details

**handlers.go** changes:
- Add `metrics *metrics.Collector` field to `Handler` struct (nil-safe: all collector methods are nil-receiver safe)
- `NewHandlerWithAuth` gains optional `*metrics.Collector` parameter — or add `SetMetricsCollector` on `Handler` (prefer setter to avoid breaking constructor signature)
- `handleEvents` (line 100-176):
  - After `h.reg.Replace(sess)`: call `h.metrics.RecordSessionStart(agentID)`
  - In `defer`: call `h.metrics.RecordSessionEnd(agentID)`
  - In the SSE write loop (line 159-163): after `WriteFrame`, call `h.metrics.RecordAgentSSEBytes(agentID, n)`
  - **Tuner push**: the `sess.down` pipe is the injection point. The tuner's `pushFn` writes an `event: tune\ndata: <JSON>\n\n` frame directly to `sess.down`. Since `handleEvents` reads from `sess.down.Read(buf)` and writes to the HTTP response, the tune frame flows through transparently. BUT: the current `WriteFrame` base64-encodes all data, so tune frames must bypass `WriteFrame`. Solution: write raw SSE text to `sess.down` pipe — the agent's `readLoop` will receive it as raw bytes and must detect `event: tune` before the base64 decode path.
- `handleUp` (line 180-246):
  - After reading body: `h.metrics.RecordAgentPost(sess.AgentID(), len(body), time.Since(start))` (add `start := time.Now()` at function entry)
  - On errors (400, 409, 413): `h.metrics.RecordError(agentID, "post_failure")`
- `handleConnect` (line 304-484):
  - In SSE loop after `WriteFrame`: `h.metrics.RecordConnectBytes(agentID, 0, n)` for downstream bytes
- `handleConnectUp` (line 489-541):
  - After successful write: `h.metrics.RecordConnectBytes(agentID, len(body), 0)` for upstream bytes

**server.go** changes:
- Add `SetMetricsCollector(c *metrics.Collector)` that sets `s.handler.metrics`
- Add `SetTunerPushFn(fn func(agentID string, params TransportParams) error)` — or combine into one setup method
- The push function implementation: find the session by agentID via `s.reg`, write `event: tune\ndata: {"concurrency":N,"batch_size":N,"compress":bool}\n\n` directly to `sess.down` pipe (raw SSE, not base64-encoded)

**session.go** changes:
- No structural changes needed. RTT is measured in `handleUp` via `time.Since(start)`, not on the session itself.
- Optionally add `PostCount() uint64` and `ErrorCount() uint64` atomic counters if the collector needs session-level stats (but the collector tracks these in-memory, so likely not needed).

**Critical design decision — SSE tune frame injection**:
The `sess.down` pipe carries raw bytes that `handleEvents` reads and wraps in `WriteFrame` (base64). To inject a tune frame, we must NOT go through `WriteFrame`. Two options:
1. **Write raw SSE to `sess.down`**: The tune frame bytes (`event: tune\ndata: {...}\n\n`) get read by `handleEvents` and passed to `WriteFrame`, which would base64-encode them — WRONG.
2. **Write directly to the HTTP `ResponseWriter`**: Requires passing the writer to the tuner, creating a tight coupling and concurrency issue — WRONG.
3. **Use a separate control channel on the Session**: Add a `tuneCh chan TransportParams` to `Session`. `handleEvents` selects on both `sess.down.Read()` and `sess.tuneCh`. When a tune arrives, write it as raw SSE directly to `w` (bypassing `WriteFrame`). This is clean and avoids corrupting the data stream. **Use this approach.**

Session changes for option 3:
- Add `tuneCh chan transportParams` (unbuffered or capacity 1) to `Session`
- Add `SendTune(params TransportParams) bool` method (non-blocking send)
- In `handleEvents`, change the read loop to `select` on both `sess.down` reads and `sess.tuneCh` receives. On tune: write raw SSE `event: tune\ndata: <JSON>\n\n` directly to `w` + `f.Flush()`.
- The read loop changes from a simple `sess.down.Read(buf)` to a select-based loop. Since `Read` with deadline is the current pattern, use a goroutine to bridge: a goroutine reads from `sess.down` into a channel, and the main loop selects on that channel + `tuneCh`.

---

## Phase 4: Agent-Side Tuning Reception

**Dependencies**: Phase 3 (server must emit tune frames)

### Files to modify

| File | Change |
|------|--------|
| `internal/transport/sse.go` | Modify `sseDecoder` to capture `event:` lines, not just ignore them |
| `internal/transport/conn.go` | Parse `event: tune` in `readLoop`, apply params to batcher/pool/gzip |
| `internal/transport/batcher.go` | Add `SetMaxSize(int)` method for dynamic batch ceiling adjustment |
| `internal/agent/agent.go` | Track which CLI flags were explicitly set (for static override) |

### Key implementation details

**sse.go** changes:
- Current behavior (line 99): `event:`, `id:`, `retry:` lines are silently dropped
- Add `eventType` field to `sseDecoder` struct
- When a line matches `event: <type>`, store the type string
- When emitting a completed event (blank line), include the event type alongside the data
- Change `Feed` return type or add a new struct: `type SSEEvent struct { Type string; Data []byte }` — return `[]SSEEvent` instead of `[][]byte`
- **Backward compatibility**: empty `Type` means a regular `data:` frame (same as today). Old agents that don't understand `event: tune` simply ignore it (unknown event type = no-op). But since we control both sides, this is fine.
- Alternative (simpler, less invasive): keep `Feed` returning `[][]byte` for data frames, add a callback `OnControl func(eventType string, data []byte)` for non-data events. The `tune` event carries data on the next `data:` line, so the decoder must associate them. **Recommended approach**: modify the decoder to return `[]SSEEvent` where `SSEEvent` has both `Type` and `Data` fields.

**conn.go** changes in `readLoop` (line 303-333):
- Change the event processing loop from `for _, ev := range events` to iterate `SSEEvent` structs
- For `ev.Type == ""` (regular data frame): write `ev.Data` to `c.down` as before
- For `ev.Type == "tune"`: parse `ev.Data` as JSON `TransportParams`, call `c.applyTune(params)`
- New method `applyTune(params TransportParams)`:
  - **Batch size**: call `c.batcher.SetMaxSize(params.BatchSize)` (new method on Batcher)
  - **Concurrency**: if `params.Concurrency` differs from current pool size, log a warning and skip (resizing the pool at runtime is complex — requires draining workers and creating new ones). Alternatively: support pool resizing by making `c.pool` a dynamically-sized worker pool. **Recommendation for v1**: log the tune, apply batch size and gzip only, defer concurrency changes to a reconnect. This keeps the implementation safe.
  - **Gzip**: set `c.gzip = params.Compress` (atomic bool swap — `c.gzip` is read in `post()` under no lock, so use `atomic.Bool`)
  - Log: `log.Printf("transport: tune applied: batch=%d compress=%v (concurrency=%d deferred)", ...)`

**batcher.go** changes:
- Add `SetMaxSize(size int)` method: `b.mu.Lock(); b.maxSize = size; b.mu.Unlock()` — thread-safe, takes effect on next batch accumulation
- The current batch in `b.buf` is not retroactively split; the new ceiling applies to subsequent `Write` calls

**agent.go** changes:
- Add fields to `Agent` struct: `BatchSizeExplicit bool`, `ConcurrencyExplicit bool` — set by CLI when `--batch-size` / `--concurrency` flags are provided
- Pass these to `transport.Config` as `DisableAutoTuneBatch bool` / `DisableAutoTuneConcurrency bool`
- In `conn.applyTune`: check these flags before applying each axis

**Critical design decision — concurrency resizing**:
The sender pool (`c.pool chan upTask`) is created at dial time with fixed capacity = concurrency. Resizing requires:
1. Stopping all workers
2. Creating a new channel with new capacity
3. Starting new workers
This is risky mid-stream. **Recommendation**: Phase 4 implements batch size + gzip tuning only. Concurrency tuning is logged but applied only on next reconnect. Document this as a known limitation.

---

## Phase 5: Console API — Metrics Endpoints

**Dependencies**: Phase 1 (collector/store for querying)

### Files to modify

| File | Change |
|------|--------|
| `internal/consoleapi/router.go` | Add 4 metrics endpoints to `NewRouter` and `API` struct |
| `internal/consoleserver/consoleserver.go` | Pass `*metrics.Collector` and `*metrics.Store` to `NewRouter` |

### Key implementation details

**router.go** changes:
- Add `collector *metrics.Collector` and `metricsStore *metrics.Store` fields to `API` struct
- Modify `NewRouter` signature: `NewRouter(store *auth.Store, reg *server.Registry, collector *metrics.Collector, metricsStore *metrics.Store) http.Handler` — or use functional options to avoid breaking existing callers
- Register 4 new routes in `NewRouter`:
  ```
  r.Handle("/api/v1/metrics/overview", adminAuth(http.HandlerFunc(api.handleMetricsOverview))).Methods("GET")
  r.Handle("/api/v1/metrics/agents", userAuth(http.HandlerFunc(api.handleMetricsAgents))).Methods("GET")
  r.Handle("/api/v1/metrics/agents/{id}", userAuth(http.HandlerFunc(api.handleMetricsAgentDetail))).Methods("GET")
  r.Handle("/api/v1/metrics/tuning", adminAuth(http.HandlerFunc(api.handleMetricsTuning))).Methods("GET")
  ```

**Endpoint implementations**:

1. `handleMetricsOverview` (admin only):
   - Call `collector.Overview()` → returns `{activeAgents, totalThroughputUp, totalThroughputDown, errorRate5min}`
   - JSON response

2. `handleMetricsAgents` (user+, scoped):
   - Call `collector.AllAgentMetrics()` → returns `[]AgentMetrics`
   - Non-admin: filter by `userID` (cross-reference sessions in `reg` to get agentID → userID mapping)
   - Each entry: agentID, current throughput, current params, last tune decision

3. `handleMetricsAgentDetail` (user+, scoped):
   - Parse `{id}` as agentID, query params `from`, `to`, `resolution`
   - Call `metricsStore.QuerySamples(agentID, from, to)` → time-series data
   - Non-admin: verify session ownership via registry
   - JSON response: `[]MetricSample`

4. `handleMetricsTuning` (admin only):
   - Query params: `agent_id` (optional filter), `limit` (default 50)
   - Call `metricsStore.QueryDecisions(agentID, limit)` → tuning history
   - JSON response: `[]TuningDecision`

**consoleserver.go** changes:
- `NewConsoleHandler` gains `collector *metrics.Collector, metricsStore *metrics.Store` parameters
- Pass them through to `consoleapi.NewRouter`

**Callers** (in `cmd/ssetunnel/main.go`):
- Update `consoleserver.NewConsoleHandler` call to pass metrics objects (or nil when metrics disabled)

---

## Phase 6: CLI Integration — Server Flags

**Dependencies**: Phase 1, Phase 2, Phase 3

### Files to modify

| File | Change |
|------|--------|
| `cmd/ssetunnel/main.go` | Add `--metrics-dir` and `--metrics-retention` flags to `runServer`; wire collector + tuner lifecycle |

### Key implementation details

**runServer** changes (after auth store setup, before HTTP serve):
- Add flags:
  ```go
  metricsDir := fs.String("metrics-dir", "", "BadgerDB directory for metrics (default: $SSETUNNEL_DATA_DIR/metrics or ./data/metrics)")
  metricsRetention := fs.Duration("metrics-retention", 7*24*time.Hour, "metrics data retention period")
  ```
- Resolve metrics dir: env `SSETUNNEL_DATA_DIR` fallback, then `./data/metrics`
- When metrics dir is set (or always, with sensible default):
  1. `store, err := metrics.OpenStore(*metricsDir)`
  2. `collector := metrics.NewCollector(store, 10*time.Second, *metricsRetention)`
  3. `srv.SetMetricsCollector(collector)`
  4. Build the tuner `pushFn` that calls `srv.SendTune(agentID, params)` (which writes to the session's tune channel)
  5. `tuner := metrics.NewAutoTuner(collector, store, pushFn, 30*time.Second)`
  6. Start tuner in a goroutine: `go tuner.Run(ctx)` — ctx cancellation stops it
  7. On shutdown: `collector.Close()`, `store.Close()`
- When `--metrics-dir` is empty AND `SSETUNNEL_DATA_DIR` is empty: metrics disabled (collector = nil, all recording calls are nil-safe no-ops)

**Agent CLI** — no new flags needed. The existing `--batch-size` and `--concurrency` flags already exist. Add hidden/internal tracking of whether they were explicitly set:
- Use `fs.Lookup("batch-size").Changed` (not available in `flag` stdlib) — alternative: compare against default value, or add explicit `bool` flags like `--auto-tune` (default true) that can disable auto-tuning entirely
- Simpler approach: always allow auto-tuning; explicit CLI values serve as the **initial** values, and the tuner can adjust from there. Document that CLI values are starting points, not overrides. **OR**: add `--no-auto-tune` flag to disable entirely.
- **Recommendation**: add `--no-auto-tune` flag to agent CLI. When set, the agent ignores `event: tune` frames.

---

## Phase 7: Frontend — Statistics Tab

**Dependencies**: Phase 5 (API endpoints must exist)

### Files to modify

| File | Change |
|------|--------|
| `frontend/console/package.json` | Add `recharts` dependency |
| `frontend/console/src/App.tsx` | Add Statistics tab (admin-only) with overview cards, per-agent detail, tuning log |

### Dependency change

- Add `recharts` to `package.json` dependencies

### Key implementation details

**Tab structure** (admin tabs become 4 tabs):
```
Sessions | Statistics | Users | Agents
```
(Statistics inserted between Sessions and Users per spec)

Non-admin users do NOT see the Statistics tab (admin-only per spec).

**New state variables**:
```typescript
const [metricsOverview, setMetricsOverview] = useState<MetricsOverview | null>(null);
const [metricsAgents, setMetricsAgents] = useState<AgentMetrics[]>([]);
const [tuningLog, setTuningLog] = useState<TuningDecision[]>([]);
const [selectedAgent, setSelectedAgent] = useState<string | null>(null);
const [agentTimeSeries, setAgentTimeSeries] = useState<MetricSample[]>([]);
```

**New interfaces**:
```typescript
interface MetricsOverview { active_agents: number; throughput_up_bps: number; throughput_down_bps: number; error_rate: number; }
interface AgentMetrics { agent_id: string; throughput_up_bps: number; throughput_down_bps: number; latency_p50_ms: number; latency_p95_ms: number; error_rate: number; active_conns: number; params: TransportParams; }
interface TransportParams { concurrency: number; batch_size: number; compress: boolean; }
interface TuningDecision { timestamp: string; agent_id: string; old_params: TransportParams; new_params: TransportParams; reason: string; }
interface MetricSample { timestamp: string; throughput_up_bps: number; throughput_down_bps: number; latency_p50_ms: number; latency_p95_ms: number; error_rate: number; }
```

**New fetch functions** (follow existing pattern with `checkAuth`):
- `fetchMetricsOverview()` → `GET /console/api/v1/metrics/overview`
- `fetchMetricsAgents()` → `GET /console/api/v1/metrics/agents`
- `fetchMetricsAgentDetail(agentID)` → `GET /console/api/v1/metrics/agents/{id}?from=...&to=...`
- `fetchTuningLog()` → `GET /console/api/v1/metrics/tuning?limit=50`

**Polling**: add metrics fetch to the existing `useEffect` interval loop (every 10s for overview/agents, every 30s for tuning log).

**Overview section** — 3 MUI `Card` components in a `Grid`:
1. Active Agents: count with `RouterIcon`
2. Total Throughput: formatted up/down with `ArrowUpward`/`ArrowDownward` icons
3. Error Rate: percentage with color coding (green < 1%, yellow 1-5%, red > 5%)

**Per-agent section** — `AdminTable` with expandable rows:
- Columns: Agent ID, Throughput (up/down), Latency (p50/p95), Error Rate, Params (concurrency/batch/compress)
- On row click: expand to show time-series charts (Recharts `LineChart`):
  - Throughput sparkline (last 30 min)
  - Latency p50/p95 sparkline
  - Error rate indicator
- Fetches detail data on expand

**Tuning log section** — `AdminTable`:
- Columns: Timestamp, Agent ID, Change (old→new formatted), Reason
- Filterable by agent ID (TextField or Select at top)
- Refresh button

**Tab index adjustment**: Update `tabIndex` logic — admin tabs shift:
- 0 = Sessions (unchanged)
- 1 = Statistics (new)
- 2 = Users (was 1)
- 3 = Agents (was 2)

---

## Cross-Cutting Concerns

### BadgerDB dependency management
- Run `go get github.com/dgraph-io/badger/v4` before Phase 1
- BadgerDB pulls in significant transitive deps; verify no conflicts with existing `go.mod`
- Consider `badger.DB` options: disable logging (`Logger: nil`), set `SyncWrites: false` for performance (metrics are not critical enough to warrant fsync on every write)

### Nil-safety pattern
All `MetricsCollector` methods must be nil-receiver safe:
```go
func (c *MetricsCollector) RecordAgentPost(agentID string, bytes int, rtt time.Duration) {
    if c == nil { return }
    // ...
}
```
This allows the server to run with `metrics = nil` when metrics are disabled, without nil-checks at every call site.

### SSE protocol extension
The `event: tune` frame format:
```
event: tune
data: {"concurrency":2,"batch_size":524288,"compress":true}

```
This is standard SSE. The current `WriteFrame` base64-encodes data — tune frames must be written raw. The Session tune channel approach (Phase 3) handles this cleanly.

### Agent backward compatibility
Old agents (pre-Phase-4) ignore unknown SSE event types (sse.go line 99 drops them). New agents understand `event: tune`. The server can safely emit tune frames even to old agents — they will simply be ignored.

### Testing integration points
- Phase 3 integration test: start server with metrics, connect agent, verify samples are recorded in BadgerDB
- Phase 4 integration test: server sends tune frame, verify agent applies new batch size
- Phase 5 integration test: `httptest` against console API with pre-populated metrics store

---

## Risk Register

| Risk | Phase | Mitigation |
|------|-------|------------|
| BadgerDB adds significant binary size (~15 MB) | 1 | Acceptable for server deployments; agent binary doesn't include BadgerDB |
| Tune frame injection corrupts SSE data stream | 3 | Use dedicated `tuneCh` on Session; never mix with data pipe reads |
| Batcher `SetMaxSize` races with in-flight `Write` | 4 | Mutex-protected; takes effect on next batch boundary, not mid-batch |
| Pool resize at runtime causes deadlocks | 4 | Defer concurrency changes to reconnect (v1 limitation) |
| Agent ignores tune due to static CLI override | 4 | Well-documented behavior; `--no-auto-tune` flag for explicit control |
| BadgerDB write contention under high agent count | 1 | Batch writes in flush goroutine; single transaction per flush interval |
| Frontend recharts adds ~200KB to bundle | 7 | Lazy-load the Statistics tab; recharts is tree-shakeable |
| Console API metrics endpoints slow with large BadgerDB | 5 | Paginate queries; limit time-series resolution; prune aggressively |
