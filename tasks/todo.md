# Todo: ssetunnel — Cycle 1 (Transport Core)

> Plan: `tasks/plan.md`. Each task: RED (failing test) → GREEN → REFACTOR
> → `go test ./... -race -count=1 && go vet ./...` before moving on.

- [x] **Step 1: Module scaffold**
  - Acceptance: builds and vets clean; subcommand dispatch stub runs
  - Verify: `go build ./... && go vet ./...`
  - Files: `go.mod`, `cmd/ssetunnel/main.go`

- [x] **Step 2: SSE codec**
  - Acceptance: round-trip incl. binary; heartbeats filtered; flush-per-frame;
    split-line reassembly; oversized-line guard
  - Verify: `go test ./internal/transport/ -run TestSSE -race`
  - Files: `internal/transport/sse.go`, `internal/transport/sse_test.go`

- [x] **Step 3: Eager-flush batcher**
  - Acceptance: size flush at maxSize; eager flush when idle; 25ms ceiling
    under saturation; no empty/double flush under `-race`; Close drains
  - Verify: `go test ./internal/transport/ -run TestBatch -race`
  - Files: `internal/transport/batcher.go`, `internal/transport/batcher_test.go`

- [x] **Step 4: Server session + registry + handlers**
  - Acceptance: POST→Read ordered; Write→SSE frames; 409 on seq gap and
    unknown session; session replacement; EOF on close; read-deadline works
  - Verify: `go test ./internal/server/ -race`
  - Files: `internal/server/session.go`, `internal/server/handlers.go`,
    `internal/server/server_test.go`

- [x] **Step 5: Agent-side net.Conn**
  - Acceptance: full-duplex echo vs real handlers; batching observed;
    close-with-unread-data doesn't hang; goroutines settle; 8-goroutine
    concurrent Write under `-race`; POST failure surfaces as Write error
  - Verify: `go test ./internal/transport/ -run TestConn -race`
  - Files: `internal/transport/conn.go`, `internal/transport/conn_test.go`

- [x] **Step 6: yamux mux**
  - Acceptance: session over real adapter; stream echo; 32 streams with one
    stalled reader (no HoL); >256KiB unread transfer (window proof)
  - Verify: `go test ./internal/mux/ -race`
  - Files: `internal/mux/mux.go`, `internal/mux/mux_test.go`

- [x] **Step 7: Server/agent wiring + e2e + binaries**
  - Acceptance: byte-exact 1MiB e2e echo; reconnect <5s after SSE kill with
    clean entry-side error; 2 concurrent connections; binaries smoke-test
  - Verify: `go test ./... -race`; `go build ./cmd/ssetunnel`; manual `nc` smoke
  - Files: `internal/server/server.go`, `internal/agent/agent.go`,
    `internal/server/e2e_test.go`, `cmd/ssetunnel/main.go`

- [x] **Step 8: Middlebox simulation**
  - Acceptance: SSE survives 3× idle-kill at 4:1 heartbeat ratio; heartbeats-off
    control dies; bulk transfer never trips body cap; reconnect after kill works
  - Verify: `go test ./... -run Middlebox -race`
  - Files: `internal/testutil/middlebox.go`, `internal/server/middlebox_test.go`

- [x] **Step 9: Bench harness (budget proof)**
  - Acceptance: all four budgets measured through latency-injecting middlebox
    and printed PASS/FAIL — p50 added latency ≤50ms; ≥5MB/s single stream;
    32-stream no-HoL; reconnect <5s ×100 cycles with goroutine/heap settle
  - Verify: `go test ./internal/transport/ -run Bench -v -timeout 10m`
  - Files: `internal/transport/bench_test.go`
