# PR #15 Review - Request Changes

## Critical Issues (MUST FIX)

### 1. Tune frame sends raw JSON but SSE decoder expects base64 — connection killed on first tune
[handlers.go#L174-L179](internal/server/handlers.go), [sse.go#L43-L54](internal/transport/sse.go), [sse.go#L135-L144](internal/transport/sse.go)

**Problem:** `WriteTuneFrame` writes raw JSON after the `data: ` prefix (line 47: `buf.Write(jsonPayload)`), but the agent's `sseDecoder.appendData()` **always** calls `base64.StdEncoding.DecodeString` on every `data:` line (line 136). JSON characters (`{`, `"`, `:`, `}`) are not valid base64, so `DecodeString` returns a sticky error that propagates to `readLoop` → `CloseWithError` → `go c.Close()` — **the agent connection is killed the instant the first tune frame arrives**.

**Fix:** Base64-encode the JSON payload in `WriteTuneFrame`, consistent with `WriteFrame`:
```go
buf.WriteString(base64.StdEncoding.EncodeToString(jsonPayload))
```

### 2. Heartbeats never sent when heartbeat > 1s — agent sessions die at middlebox idle timeout
[handlers.go#L186-L216](internal/server/handlers.go)

**Problem:** The tune-check deadline caps at 1 second, but the timeout handler always classifies it as a tune-check timeout:
```go
tuneDeadline := h.heartbeat        // 15s default
if tuneDeadline > time.Second {
    tuneDeadline = time.Second     // capped at 1s
}
// ... read times out after 1s ...
if tuneDeadline < h.heartbeat {    // 1s < 15s → ALWAYS true
    continue                        // loops back, NEVER reaches WriteHeartbeat
}
```
Every heartbeat timeout is misclassified as a tune-check timeout. Heartbeats are **never emitted**. Middleboxes (proxies, load balancers) see the SSE stream as idle and terminate it, typically within 60s–5min, killing all agent sessions. Tests pass because they use 10ms heartbeat where `tuneDeadline == h.heartbeat`.

**Fix:** Use separate deadlines for tune-check and heartbeat. For example, track the last heartbeat time and only emit a heartbeat when `heartbeat` has actually elapsed. Or use two separate timers.

### 3. Rolling window never resets after flush — unbounded memory growth and incorrect throughput
[collector.go#L104-L128](internal/metrics/collector.go)

**Problem:** `flush()` aggregates windows into samples but **never clears or resets** the rolling window fields (`posts`, `sseBytes`, `connectUp`, `connectDn`, `errors`). The `windowStart` field exists but is never used. All subsequent aggregations accumulate over the agent's **entire lifetime**, not a rolling window. Throughput calculation (`totalBytes / flushInterval.Seconds()`) becomes increasingly inflated as data accumulates — the tuner makes decisions on wrong numbers.

**Fix:** After aggregating, reset the window fields. Either clear them inline in `flush()` or swap in a fresh `rollingWindow`.

### 4. `flushLoop` panics on nil store — crash in memory-only mode
[collector.go#L97](internal/metrics/collector.go)

**Problem:** `NewCollector` documents "Pass nil for store to run in memory-only mode." But `flushLoop` calls `c.store.PruneOlderThan(...)` directly at line 97. While `PruneOlderThan` has a nil-receiver guard, calling a method on a nil `*Store` pointer **still panics in Go** (the method set is on `*Store`, not `Store`).

**Fix:** Guard with `if c.store != nil` before the prune call.

### 5. `evaluateAll` panics on nil store — crash when store is nil
[tuner.go#L94](internal/metrics/tuner.go)

**Problem:** `_ = t.store.WriteDecision(*decision)` panics when `t.store` is nil (documented as valid: "Pass nil for store to skip persisting decisions").

**Fix:** Guard with `if t.store != nil`.

## Warnings (SHOULD FIX)

### 6. `ApplyTune` ignores compression flag changes
[conn.go#L309-L315](internal/transport/conn.go)

**Problem:** `ApplyTune(batchSize int, compress bool)` accepts `compress` but never updates `c.gzip`. The tuner's Priority 3 compression decisions are silently dropped. The comment at line 313-314 says "gzip flag change takes effect on next batch flush" but this isn't implemented.

**Fix:** Update `c.gzip` in `ApplyTune` with appropriate synchronization (it's read in `post()`).

### 7. Metrics overview ErrorRate always zero for scoped queries
[router.go#L893-L904](internal/consoleapi/router.go)

**Problem:** `handleMetricsOverview` re-computes throughput for user-scoped agents but never accumulates `ErrorRate`. The field stays at 0.0 regardless of actual errors.

**Fix:** Accumulate error rates from `AllAgentMetrics()` snapshots when re-computing the scoped overview.

### 8. BadgerDB errors silently discarded
[collector.go#L97](internal/metrics/collector.go), [collector.go#L122](internal/metrics/collector.go), [tuner.go#L94](internal/metrics/tuner.go)

**Problem:** Three BadgerDB write paths use `_ = ` to discard errors. A full disk or corrupt DB would go undetected — unbounded disk growth, lost samples, lost decisions.

**Fix:** At minimum, `log.Printf` the errors.

### 9. Throughput P50/P95 are identical values (not real percentiles)
[collector.go#L437-L440](internal/metrics/collector.go)

**Problem:** `snapshotWindow` sets `ThroughputUpP50 = ThroughputUpP95 = throughputUp` (a single computed value). The field names misrepresent the data. The tuner uses `ThroughputUpP50` thinking it's a percentile.

**Fix:** Rename fields or compute actual percentiles from multiple samples.

## Suggestions (CONSIDER)

### 10. `windowStart` field is dead code
[collector.go#L28](internal/metrics/collector.go) — Set at creation but never read or updated. Remove or use for window pruning.

### 11. Redundant `Overview()` call in `handleMetricsOverview`
[router.go#L891](internal/consoleapi/router.go) — `a.mc.Overview()` result is discarded when `len(agentIDs) > 0`. Remove the redundant call to avoid double lock contention.

### 12. `Evaluate()` has non-atomic lock pattern across multiple acquire/release cycles
[tuner.go#L128-L193](internal/metrics/tuner.go) — Acquires/releases `t.mu` up to 5 times per call. Safe today since `evaluateAll` is sequential, but exported `Evaluate` could be called concurrently in the future.

### 13. SetAuthStore/SetMetricsCollector ordering fragility
[server.go#L61-L78](internal/server/server.go) — Both methods rebuild the handler and copy `OnUpPush` from `prev`, but the ordering of calls matters. Works today but fragile.

## Summary of Changes
- **New metrics package** (`internal/metrics/`): BadgerDB-backed time-series store, per-agent rolling window collector, and auto-tuner with priority-based heuristic evaluation (batch → concurrency → compression).
- **Server integration**: Metrics recording wired into all tunnel handlers; SSE tune frame injection via non-blocking select + read deadline pattern; session `tuneCh` channel for delivering tune frames.
- **Agent-side reception**: SSE decoder extended with typed `SSEEvent{Type, Data}`; `Conn.OnTune` callback dispatches tune frames; `ApplyTune` adjusts batch size in real-time; `--no-auto-tune` flag.
- **Console API**: 4 new endpoints (overview, agents, samples, decisions) with user-scoped visibility.
- **Frontend**: Statistics tab with recharts line charts, overview cards, per-agent metrics, and tuning decision history.
