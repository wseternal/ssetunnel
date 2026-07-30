# Spec: Auto-Tuning Metrics & Statistics Console

## Objective

The server continuously monitors per-agent and per-connect-client transport metrics, automatically tunes transport parameters (concurrency, batch size, compression) based on observed performance, persists both metrics and tuning decisions to BadgerDB, and exposes a statistics console page showing the data, current parameters, and the tuner's reasoning.

**User story:** As the tunnel operator, I want to see how each agent and connection is performing, understand what the auto-tuner changed and why, and have agents self-optimize without manual `--batch-size` / `--concurrency` flags.

## Tech Stack

- **Go** (server-side metrics collection, auto-tuner, BadgerDB storage, console API)
- **BadgerDB** (`github.com/dgraph-io/badger/v4`) — embedded KV store for time-series metrics and tuning decisions
- **React + TypeScript + MUI v9 + orca-ui** (frontend statistics page)
- **Recharts** or lightweight charting (frontend time-series visualization)

## Commands

```bash
go build ./...                                          # compile check
go test ./internal/metrics/... -v -timeout 30s          # metrics package tests
go test ./... -timeout 120s                             # full suite
cd frontend/console && npx vite build                   # rebuild frontend
./local.sh server --disable-auth                        # dev server
```

## Assumptions

1. BadgerDB data directory defaults to `$SSETUNNEL_DATA_DIR/metrics` or `./data/metrics`; configurable via `--metrics-dir` flag.
2. Metrics retention is 7 days by default (configurable via `--metrics-retention` flag).
3. The auto-tuner runs on a periodic evaluation interval (default 30s).
4. Parameter push to agents uses a new SSE control frame (`event: tune`) on the existing `/events` downstream — no new HTTP endpoint needed.
5. Static CLI flags (`--batch-size`, `--concurrency`) remain as overrides that disable auto-tuning for that axis.
6. Connect clients do NOT receive tuning (they are short-lived and user-initiated).
7. No external time-series DB (Prometheus/Grafana) — BadgerDB only.

## Architecture

```
Server Handler
  │
  ├── /events (agent SSE)  ──→ MetricsCollector.Record()  ──→ BadgerDB (time-series)
  ├── /up (agent POST)     ──→ MetricsCollector.Record()
  ├── /connect (client SSE) ──→ MetricsCollector.Record()
  └── /connect-up           ──→ MetricsCollector.Record()
                                       │
                                       ▼
                               AutoTuner (30s tick)
                                   │
                                   ├── evaluate(agentID) → Decision { params, reason }
                                   ├── persist decision → BadgerDB
                                   └── push via SSE control frame → agent adjusts
                                       │
                                       ▼
Console API (GET /api/v1/metrics/*)
  │
  ├── /metrics/overview    → global summary (agents, throughput, errors)
  ├── /metrics/agents      → per-agent current metrics + current params
  ├── /metrics/agents/{id} → per-agent time-series (throughput, latency)
  └── /metrics/tuning      → tuning decision history (what, when, why)
                                       │
                                       ▼
Frontend Statistics Tab
  ├── Overview cards (active agents, total throughput, error rate)
  ├── Per-agent detail panel (throughput chart, current params)
  └── Tuning log table (timestamp, agent, change, reason)
```

## Project Structure

```
internal/metrics/
├── collector.go        # MetricsCollector: record + query metrics
├── collector_test.go
├── store.go            # BadgerDB persistence (time-series + decisions)
├── store_test.go
├── tuner.go            # AutoTuner: evaluate + decide + push
├── tuner_test.go
├── types.go            # MetricSample, TuningDecision, AgentMetrics
└── doc.go

internal/server/        # modified
├── handlers.go         # hook MetricsCollector into /events, /up, /connect, /connect-up
└── session.go          # expose RTT, error counters

internal/consoleapi/    # modified
└── router.go           # new metrics endpoints

frontend/console/src/   # modified
└── App.tsx             # new Statistics tab with charts
```

## Code Style

```go
// MetricSample is one time-series data point recorded by the collector.
type MetricSample struct {
    Timestamp    time.Time `json:"timestamp"`
    AgentID      string    `json:"agent_id"`
    BytesUp      uint64    `json:"bytes_up"`
    BytesDown    uint64    `json:"bytes_down"`
    ThroughputUp float64   `json:"throughput_up_bps"`   // bytes/sec
    ThroughputDn float64   `json:"throughput_dn_bps"`   // bytes/sec
    LatencyP50   float64   `json:"latency_p50_ms"`
    LatencyP95   float64   `json:"latency_p95_ms"`
    ErrorRate    float64   `json:"error_rate"`          // 0.0–1.0
    ActiveConns  int       `json:"active_conns"`
}

// TuningDecision is one auto-tuner output with reasoning.
type TuningDecision struct {
    Timestamp  time.Time         `json:"timestamp"`
    AgentID    string            `json:"agent_id"`
    OldParams  TransportParams   `json:"old_params"`
    NewParams  TransportParams   `json:"new_params"`
    Reason     string            `json:"reason"`     // human-readable explanation
    Metrics    MetricSnapshot    `json:"metrics"`    // snapshot that triggered it
}

type TransportParams struct {
    Concurrency int  `json:"concurrency"`
    BatchSize   int  `json:"batch_size"`
    Compress    bool `json:"compress"`
}
```

## Detailed Design

### 1. MetricsCollector (`internal/metrics/collector.go`)

Thread-safe in-memory accumulator with periodic flush to BadgerDB.

**Recording hooks** (called from server handlers):
- `RecordAgentPost(agentID string, bytes int, rtt time.Duration)` — each `/up` POST
- `RecordAgentSSEBytes(agentID string, bytes int)` — each `/events` SSE frame write
- `RecordConnectBytes(agentID string, up, down int)` — connect client traffic
- `RecordSessionStart(agentID string)` / `RecordSessionEnd(agentID string)`
- `RecordError(agentID string, kind string)` — POST failures, session deaths

**Rolling windows** (in-memory, for the tuner):
- Per-agent: 5-minute sliding window of throughput, latency percentiles, error counts
- Global: aggregate totals for the overview page

**Flush** (every 10s):
- Aggregate the last flush interval into one `MetricSample` per agent
- Write to BadgerDB with key `m:<agentID>:<unix_nano>` (sorted for range scans)
- Prune entries older than retention period

### 2. BadgerDB Store (`internal/metrics/store.go`)

```go
type Store struct {
    db *badger.DB
}

func OpenStore(dir string) (*Store, error)
func (s *Store) WriteSamples(samples []MetricSample) error
func (s *Store) QuerySamples(agentID string, from, to time.Time) ([]MetricSample, error)
func (s *Store) WriteDecision(d TuningDecision) error
func (s *Store) QueryDecisions(agentID string, limit int) ([]TuningDecision, error)
func (s *Store) PruneOlderThan(retention time.Duration) error
func (s *Store) Close() error
```

Key scheme:
- Metric samples: `m:<agentID>:<unix_nano>` → JSON-encoded `MetricSample`
- Tuning decisions: `t:<agentID>:<unix_nano>` → JSON-encoded `TuningDecision`
- Rolling window: `w:<agentID>` → JSON-encoded window state (restored on server restart)
- Sorted lexicographically = chronological within an agent

### 3. AutoTuner (`internal/metrics/tuner.go`)

```go
type AutoTuner struct {
    collector *Collector
    store     *Store
    pushFn    func(agentID string, params TransportParams) error // SSE push
    interval  time.Duration
}

func (t *AutoTuner) Run(ctx context.Context)  // periodic evaluation loop
func (t *AutoTuner) Evaluate(agentID string) (*TuningDecision, error)
```

**Decision logic** (per agent, each tick):

1. Read the 5-minute rolling window for this agent
2. **Throughput saturation**: if throughput is within 80% of the current batch ceiling → increase batch size (up to server max 1 MiB). If throughput is < 30% of ceiling for 2+ consecutive evaluations → decrease batch size (floor: 4 KiB).
3. **Latency-driven concurrency**: if p95 POST RTT > 500ms and throughput is not scaling with concurrency → increase concurrency (up to server max 4). If error rate > 5% and concurrency > 1 → decrease concurrency (backpressure relief).
4. **Compression**: if throughput_up < 100 KB/s and error_rate is low → enable gzip (low-bandwidth links benefit most). If throughput_up > 1 MB/s → disable gzip (compression CPU cost outweighs wire savings on fast links).
5. **Stability guard**: do not change more than one parameter per evaluation (prevents oscillation). Minimum 2 minutes between decisions per agent.

**Push mechanism**: The tuner calls `pushFn(agentID, params)` which writes an SSE control frame (`event: tune\ndata: <JSON params>\n\n`) on the agent's active `/events` downstream. The agent's `Conn.readLoop` parses this and adjusts `Batcher.maxSize`, pool depth, and gzip flag.

### 4. Agent-Side Tuning Reception (`internal/transport/conn.go` modification)

- `Conn.readLoop` already parses SSE events; add handling for `event: tune` control frames
- On receiving `tune`, update `Batcher` max size, sender pool depth, and gzip flag
- Emit a log line for observability
- If the agent was started with explicit `--batch-size` / `--concurrency` flags, ignore the tune for that axis (static override)

### 5. Server Integration (`internal/server/handlers.go` modification)

- `Handler` gains a `metrics *metrics.Collector` field (nil-safe: no-op when not configured)
- `handleEvents`: call `metrics.RecordSessionStart/End`, record SSE bytes per frame
- `handleUp`: call `metrics.RecordAgentPost` with body size and POST RTT
- `handleConnect`/`handleConnectUp`: call `metrics.RecordConnectBytes`
- The `Handler.pushFn` for the tuner writes to the session's `down` pipe with a special SSE control frame prefix

### 6. Console API (`internal/consoleapi/router.go` addition)

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/v1/metrics/overview` | Admin | Global summary: active agents, total throughput, error rate |
| GET | `/api/v1/metrics/agents` | User+ | Per-agent current metrics + current transport params (filtered by `user_id` for non-admin) |
| GET | `/api/v1/metrics/agents/{id}` | User+ | Per-agent time-series (query params: `from`, `to`, `resolution`; non-admin sees own agents only) |
| GET | `/api/v1/metrics/tuning` | Admin | Tuning decision history (query params: `agent_id`, `limit`) |

### 7. Frontend Statistics Tab (`frontend/console/src/App.tsx`)

New tab: **Statistics** (admin-only, between Sessions and Users)

**Overview section**: 3 cards — Active Agents, Total Throughput (up/down), Error Rate (last 5 min)

**Per-agent section**: Expandable rows per agent showing:
- Current transport params (concurrency, batch size, compress) — live from the tuner
- Throughput sparkline (last 30 min)
- Latency p50/p95 sparkline
- Error rate indicator

**Tuning log section**: AdminTable of recent tuning decisions:
- Columns: Timestamp, Agent, Change (old→new), Reason
- Filterable by agent ID

## Testing Strategy

- **Unit tests** (`collector_test.go`): Verify recording, rolling window aggregation, sample generation
- **Unit tests** (`store_test.go`): BadgerDB write/query/prune with temp directory
- **Unit tests** (`tuner_test.go`): Decision logic with synthetic metric inputs — test each heuristic branch (saturation, latency, compression, stability guard)
- **Integration test**: End-to-end with a real server + agent — verify that metrics are recorded, tuner fires, agent receives tune frame, and console API returns data
- **Frontend**: Manual verification of the statistics tab with real data

## Boundaries

- **Always**: Record metrics for every POST and SSE frame; persist to BadgerDB; never drop tuning decisions
- **Ask first**: Changing the tuning heuristics thresholds; modifying the SSE wire protocol for control frames; adding new BadgerDB key prefixes
- **Never**: Auto-tune connect clients; override explicit CLI flags; use external time-series databases; break backward compatibility with agents that don't understand `event: tune`

## Success Criteria

1. Every agent POST and SSE frame is recorded as a metric sample in BadgerDB
2. The auto-tuner evaluates each active agent every 30s and produces at most one decision per 2 minutes per agent
3. Tuning decisions are persisted to BadgerDB with human-readable reasoning
4. Agents receive and apply tuning within 5s of the decision
5. The console Statistics tab shows real-time overview, per-agent detail, and tuning history
6. Static CLI flags (`--batch-size`, `--concurrency`) take precedence over auto-tuning for that axis
7. BadgerDB data older than the retention period is pruned automatically
8. Server restarts preserve all persisted metrics and tuning history

## Open Questions

1. ~~Should the SSE control frame use `event: tune` or a new `X-SSET-` wire mechanism?~~ **Resolved: `event: tune`** — standard SSE event type, readLoop already parses events, backward-compatible (old agents ignore unknown event types).
2. ~~Should the tuner's 5-minute rolling window also be persisted, or is BadgerDB only for the aggregated samples?~~ **Resolved: persist both** — rolling window state is written to BadgerDB on each flush, so the tuner can resume after server restart without a cold-start gap. Key: `w:<agentID>` → JSON-encoded window state.
3. ~~Should non-admin users see their own agent's metrics (scoped by `user_id`), or is this admin-only?~~ **Resolved: scoped visibility** — non-admin users see metrics for sessions attributed to their own `user_id`. Overview and tuning history are admin-only. Per-agent detail is filtered by session ownership.
