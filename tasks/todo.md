# Auto-Tuning Metrics — Task List

**Spec**: `docs/specs/auto_tuning_metrics.md`
**Plan**: `tasks/plan.md`

---

## Phase 1: Foundation

- [ ] Task: Add BadgerDB dependency (`go get github.com/dgraph-io/badger/v4`)
  - Acceptance: `go.mod` includes badger/v4, `go build ./...` passes
  - Verify: `go build ./...`
  - Files: `go.mod`, `go.sum`

- [ ] Task: Create `internal/metrics/types.go` with MetricSample, TuningDecision, TransportParams, MetricSnapshot, AgentMetrics
  - Acceptance: All types compile, JSON tags match spec
  - Verify: `go build ./internal/metrics/...`
  - Files: `internal/metrics/types.go`, `internal/metrics/doc.go`

- [ ] Task: Create `internal/metrics/store.go` — BadgerDB persistence (Open, WriteSamples, QuerySamples, WriteDecision, QueryDecisions, PruneOlderThan, WriteWindow, ReadWindow, Close)
  - Acceptance: All CRUD operations work; key scheme `m:`, `t:`, `w:` with zero-padded timestamps
  - Verify: `go test ./internal/metrics/... -run TestStore -v`
  - Files: `internal/metrics/store.go`, `internal/metrics/store_test.go`

- [ ] Task: Create `internal/metrics/collector.go` — MetricsCollector with recording methods, rolling window, periodic flush, query methods
  - Acceptance: RecordAgentPost, RecordAgentSSEBytes, RecordConnectBytes, RecordSessionStart/End, RecordError all nil-safe; flush writes to store; Overview/AgentMetrics/AllAgentMetrics queries work
  - Verify: `go test ./internal/metrics/... -run TestCollector -v`
  - Files: `internal/metrics/collector.go`, `internal/metrics/collector_test.go`

## Phase 2: Auto-Tuner

- [ ] Task: Create `internal/metrics/tuner.go` — AutoTuner with evaluation loop, decision logic, stability guard, SSE push
  - Acceptance: Each heuristic branch tested (throughput saturation, latency concurrency, compression, stability guard, one-param-per-eval)
  - Verify: `go test ./internal/metrics/... -run TestTuner -v`
  - Files: `internal/metrics/tuner.go`, `internal/metrics/tuner_test.go`

## Phase 3: Server Integration

- [ ] Task: Add `tuneCh` channel and `SendTune` method to Session
  - Acceptance: `Session.SendTune(params)` does non-blocking send; tunable params are serializable
  - Verify: `go build ./internal/server/...`
  - Files: `internal/server/session.go`

- [ ] Task: Restructure `handleEvents` SSE loop to select on both `sess.down` and `sess.tuneCh`
  - Acceptance: Regular SSE data frames flow unchanged; tune frames written as raw SSE `event: tune\ndata: <JSON>\n\n`; no data corruption
  - Verify: `go test ./internal/server/... -run TestHandleEvents -v` + manual e2e test
  - Files: `internal/server/handlers.go`

- [ ] Task: Wire metrics recording into `handleUp`, `handleEvents`, `handleConnect`, `handleConnectUp`
  - Acceptance: Every POST records size+RTT; every SSE frame records bytes; session start/end tracked; errors tracked
  - Verify: `go test ./internal/server/... -v -timeout 30s`
  - Files: `internal/server/handlers.go`

- [ ] Task: Add `SetMetricsCollector` to Server; wire tuner pushFn via registry lookup + `SendTune`
  - Acceptance: Server can inject collector; pushFn finds session by agentID and calls SendTune
  - Verify: `go build ./...`
  - Files: `internal/server/server.go`

## Phase 4: Agent-Side Tuning Reception

- [ ] Task: Modify `sseDecoder` in `internal/transport/sse.go` to capture `event:` type, return `[]SSEEvent{Type, Data}`
  - Acceptance: Regular data frames have `Type==""`; tune frames have `Type=="tune"`; backward compatible
  - Verify: `go test ./internal/transport/... -run TestSSE -v`
  - Files: `internal/transport/sse.go`, `internal/transport/sse_test.go`

- [ ] Task: Add `SetMaxSize` method to Batcher
  - Acceptance: Thread-safe; takes effect on next batch boundary
  - Verify: `go test ./internal/transport/... -run TestBatcher -v`
  - Files: `internal/transport/batcher.go`, `internal/transport/batcher_test.go`

- [ ] Task: Add `applyTune` to `Conn` — parse tune event in readLoop, adjust batcher max size and gzip; defer concurrency
  - Acceptance: `event: tune` parsed and applied; batch size changes; gzip toggles; concurrency logged but deferred; explicit CLI flags respected
  - Verify: `go test ./internal/transport/... -run TestConn -v`
  - Files: `internal/transport/conn.go`, `internal/transport/conn_test.go`

- [ ] Task: Add `--no-auto-tune` flag to agent CLI; pass to transport.Config
  - Acceptance: Agent ignores tune frames when flag set; default behavior allows tuning
  - Verify: `go build ./cmd/ssetunnel/...`
  - Files: `internal/agent/agent.go`, `cmd/ssetunnel/main.go`

## Phase 5: Console API

- [ ] Task: Add 4 metrics endpoints to `internal/consoleapi/router.go` — overview, agents, agents/{id}, tuning
  - Acceptance: All endpoints return correct JSON; admin-only vs user-scoped auth; nil-safe when metrics disabled
  - Verify: `go test ./internal/consoleapi/... -v -timeout 30s`
  - Files: `internal/consoleapi/router.go`, `internal/consoleapi/consoleapi_test.go`

- [ ] Task: Pass metrics objects through `consoleserver.NewConsoleHandler`
  - Acceptance: Metrics collector and store reach the API router; nil when disabled
  - Verify: `go build ./...`
  - Files: `internal/consoleserver/consoleserver.go`, `cmd/ssetunnel/main.go`

## Phase 6: CLI Integration

- [ ] Task: Add `--metrics-dir` and `--metrics-retention` flags to `runServer`; wire collector + tuner lifecycle (open store, start collector flush, start tuner goroutine, shutdown cleanup)
  - Acceptance: Server starts with metrics enabled; BadgerDB created at specified dir; tuner runs; clean shutdown closes store
  - Verify: `go build ./cmd/ssetunnel/...` + manual test with `./local.sh server --metrics-dir /tmp/metrics`
  - Files: `cmd/ssetunnel/main.go`

## Phase 7: Frontend

- [ ] Task: Add `recharts` dependency to frontend
  - Acceptance: `npm install` succeeds; recharts importable
  - Verify: `cd frontend/console && npx vite build`
  - Files: `frontend/console/package.json`

- [ ] Task: Add Statistics tab to App.tsx — overview cards, per-agent table with expandable charts, tuning log table
  - Acceptance: Admin sees Statistics tab between Sessions and Users; overview cards show live data; per-agent rows expand with Recharts sparklines; tuning log shows decision history
  - Verify: `cd frontend/console && npx vite build` + manual browser test
  - Files: `frontend/console/src/App.tsx`

- [ ] Task: Rebuild frontend dist and verify embedding
  - Acceptance: `frontend/console/dist/` updated; `go build ./...` embeds it
  - Verify: `go build ./... && ./local.sh server --disable-auth` + browser check
  - Files: `frontend/console/dist/*`
# Todo List - ssetunnel Cycle 3

- [x] Slice 1: Database Schema & Migrations (`schema.hcl`, `atlas.hcl`, `migrations/`)
  - Acceptance: `schema.hcl` defines `tokens`, `pins`, `admin_sessions`; migration SQL files generated in `migrations/` and embedded via `//go:embed`.
  - Verify: `atlas migrate validate` / `go test ./migrations/...` passes.
  - Files: `schema.hcl`, `atlas.hcl`, `migrations/migrations.go`

- [x] Slice 2: Auth Storage Engine (`internal/auth/`)
  - Acceptance: `store.go` implements PostgreSQL operations with SHA-256 digest hashing & `sync.Map` read-through cache; `totp.go`, `pin.go`, `token.go` pass unit tests.
  - Verify: `go test ./internal/auth/... -race -count=1` passes with testcontainers (`postgres:tc:`).
  - Files: `internal/auth/store.go`, `internal/auth/totp.go`, `internal/auth/pin.go`, `internal/auth/token.go`, `internal/auth/store_test.go`

- [x] Slice 3: Server Middleware & HTTP Auth Enforcement (`internal/server/`)
  - Acceptance: `/events` and `/up` endpoints reject unauthorized requests with 401 when auth enabled, and bypass when `--disable-auth`.
  - Verify: `go test ./internal/server/... -run Auth -race -count=1` passes.
  - Files: `internal/server/middleware.go`, `internal/server/handlers.go`, `internal/server/server.go`

- [x] Slice 4: TCP Entry Listener Handshake & Buffer Pool (`internal/server/`)
  - Acceptance: Entry listener validates `<token>\n` within 5s timeout, replies `OK\n`, and uses `sync.Pool` 32KB buffers for zero-alloc `io.CopyBuffer` stream proxying.
  - Verify: `go test ./internal/server/... -run EntryHandshake -race -count=1` passes.
  - Files: `internal/server/server.go`, `internal/server/session.go`

- [x] Slice 5: Management JSON API (`internal/consoleapi/`)
  - Acceptance: `/api/v1/login`, `/api/v1/tokens`, `/api/v1/enroll`, `/api/v1/sessions` function correctly with admin TOTP cookie auth and in-memory session stats.
  - Verify: `go test ./internal/consoleapi/... -race -count=1` passes.
  - Files: `internal/consoleapi/router.go`, `internal/consoleapi/consoleapi_test.go`

- [x] Slice 6: Embedded React Console SPA & litespaserver Integration (`frontend/`, `internal/consoleserver/`)
  - Acceptance: React 18 + Vite + MUI console builds into `frontend/console/dist`, embeds via `frontend/frontend.go`, and is served via `litespaserver`.
  - Verify: `cd frontend/console && npm run build` succeeds; `go test ./internal/server/... -race -count=1` passes.
  - Files: `frontend/console/...`, `frontend/frontend.go`, `internal/consoleserver/consoleserver.go`

- [x] Slice 7: Connect Client Wrapper & Subcommand Integration (`internal/connect/`, `cmd/`)
  - Acceptance: `ssetunnel connect` supports Local Port and Stdio (`--local -`) modes, injecting `<token>\n` handshake; `agent` passes token in HTTP headers.
  - Verify: `go test ./internal/connect/... -race -count=1` passes; `go test ./cmd/ssetunnel/... -race -count=1` passes.
  - Files: `internal/connect/client.go`, `internal/transport/conn.go`, `internal/agent/agent.go`, `cmd/ssetunnel/main.go`

- [x] Slice 8: End-to-End Integration Tests (`internal/server/`)
  - Acceptance: Full E2E flow passes: Admin Login → PIN Generation → Agent Enrollment → Client Wrapper TCP Proxying.
  - Verify: `go test ./internal/server/... -run E2E_Cycle3 -race -count=1` passes.
  - Files: `internal/server/e2e_cycle3_test.go`
