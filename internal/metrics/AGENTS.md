# Metrics

Per-agent transport metrics collection, rolling window aggregation, BadgerDB persistence, and auto-tuning. Provides the data layer for the Statistics console tab and the `AutoTuner` that adjusts transport parameters at runtime.

## Architecture

```
Server handlers                     MetricsCollector                  AutoTuner
───────────────                     ────────────────                  ─────────
RecordAgentPost(agent, bytes, rtt) → rollingWindow (per-agent)
RecordAgentSSEBytes(agent, bytes)  →   posts[], sseBytes, errors
RecordConnectBytes(agent, up, dn)  →   connectUp, connectDn
RecordSessionStart/End(agent)      →   activeConn (gauge)
RecordError(agent, kind)           →   errors++
                                     ↓ flushLoop (every flushInterval)
                                   aggregateWindow() → MetricSample → Store.WriteSamples
                                   pruneOlderThan(retention)
                                     ↓
                                   AgentSnapshot() ←────────── Evaluate(agentID)
                                                              ↓ threshold checks
                                                           TuningDecision → pushFn → agent
```

## Core Types

### `MetricsCollector`
Central hub. Maintains per-agent `rollingWindow` structs, periodically flushes aggregated `MetricSample`s to BadgerDB, and exposes query methods for the console API. All recording methods are **nil-receiver safe** (no-op on nil collector).

| Method | Purpose |
|--------|---------|
| `RecordAgentPost(agentID, bytes, rtt)` | Record one upstream POST |
| `RecordAgentSSEBytes(agentID, bytes)` | Record downstream SSE bytes |
| `RecordConnectBytes(agentID, up, down)` | Record connect client traffic |
| `RecordSessionStart/End(agentID)` | Active connection gauge |
| `RecordError(agentID, kind)` | Error counter |
| `Overview()` | Global summary (active agents, throughput, error rate) |
| `AllAgentMetrics()` | Per-agent `AgentMetrics` for console API |
| `AgentSnapshot(agentID)` | Current `MetricSnapshot` for tuner evaluation |
| `SetParams/GetParams(agentID, params)` | Current `TransportParams` (updated by tuner) |
| `SetLastDecision(decision)` | Last `TuningDecision` per agent |

**Configuration**: `flushInterval` (default 10s), `retention` (default 7 days). Pass nil store for memory-only mode.

### `Store` (BadgerDB)
Persists metric samples, tuning decisions, and rolling window state. Keys are lexicographically sorted for chronological prefix scans.

| Key Pattern | Prefix | Content |
|-------------|--------|---------|
| `m:<agentID>:<020d unix_nano>` | `m:` | MetricSample (JSON) |
| `t:<agentID>:<020d unix_nano>` | `t:` | TuningDecision (JSON) |
| `w:<agentID>` | `w:` | Rolling window state |

**Methods**: `WriteSamples`, `QuerySamples(agentID, from, to)`, `WriteDecision`, `QueryDecisions(agentID, limit)`, `PruneOlderThan(retention)`, `WriteWindow/ReadWindow`.

**Configuration**: `SyncWrites: false` (metrics are not critical for fsync), `CompactL0OnClose: true`.

### `AutoTuner`
Periodically evaluates each active agent's performance and pushes transport parameter adjustments via SSE `event: tune` frames. Runs as a blocking `Run(ctx)` loop.

**Evaluation priority** (one change per cycle):
1. **Throughput saturation → batch size**: ratio > 80% → double (cap 1 MiB); ratio < 30% for 2+ consecutive evals → halve (floor 4 KiB)
2. **Latency → concurrency**: p95 > 500ms → increment (cap 4); only if batch wasn't changed
3. **Error rate → concurrency**: error rate > 5% → decrement (floor 1); only if batch wasn't changed
4. **Bandwidth → compression**: < 100 KB/s → enable gzip; > 1 MB/s → disable gzip; only if neither batch nor concurrency changed

**Stability guard**: 2-minute minimum interval between decisions per agent.

**Undersaturation tracking**: Requires 2 consecutive undersaturated evaluations before decreasing batch size (prevents oscillation on transient dips).

### Types

| Type | Purpose |
|------|---------|
| `MetricSample` | One time-series data point per agent per flush (bytes, throughput, latency percentiles, error rate) |
| `TransportParams` | Tunable parameters: `Concurrency`, `BatchSize`, `Compress` |
| `TuningDecision` | One tuner output: old→new params, reason, triggering snapshot |
| `MetricSnapshot` | Aggregated rolling window view (throughput P50/P95, latency P50/P95, error rate, active conns) |
| `AgentMetrics` | Per-agent state for console: snapshot + current params + last decision |
| `Overview` | Global summary: active agents, aggregate throughput, error rate |

## Rules
* All `MetricsCollector` recording methods are nil-receiver safe — callers never need nil checks.
* Rolling windows are **reset after each flush** so each interval produces an independent sample. Active connection gauges are preserved across resets.
* `MetricSnapshot.ThroughputUpP50/P95` hold the same aggregate value (not statistical percentiles) — naming is for API compatibility.
* AutoTuner evaluates one parameter change per cycle (priority order: batch > concurrency > compression).
* The `pushFn` callback delivers decisions to agents via SSE; it is called synchronously within `evaluateAll`.
* Store operations are nil-safe — pass nil store to disable persistence.
