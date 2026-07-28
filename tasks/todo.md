# Todo: ssetunnel — Cycle 2 (Upstream Throughput)

> Plan: `tasks/plan.md`. Per task: RED → GREEN → REFACTOR →
> `go test ./... -race -count=1 && go vet ./...` before moving on.

- [x] **Step 1: Reorder window pure core**
  - Acceptance: 8! permutation property test byte-exact; dupes dropped;
    window-full + gap-timeout errors; in-order passthrough; `-count=20` green
  - Verify: `go test ./internal/transport/ -run TestReorder -race -count=20`
  - Files: `internal/transport/reorder.go`, `internal/transport/reorder_test.go`

- [x] **Step 2: Server window-gated push + caps + gzip decode**
  - Acceptance: shuffled POSTs (deterministic release-gate) reassemble
    byte-exact; legacy path verbatim and green; caps header well-formed;
    gzip round-trip; 400 on unknown flag / non-negotiated gzip; 1MiB
    boundary accepted; gap timeout kills session
  - Verify: `go test ./internal/server/ -race -count=5`
  - Files: `internal/server/session.go`, `internal/server/handlers.go`,
    `internal/server/server_test.go`

- [x] **Step 3: Compat matrix part 1 (old agent × new server)**
  - Acceptance: caps-less agent × full-caps server echoes 1MiB byte-exact;
    server uses legacy no-window path for that session
  - Verify: `go test ./internal/server/ -run TestCompat -race`
  - Files: `internal/testutil/middlebox.go`, `internal/server/compat_test.go`

- [x] **Step 4: Agent sender pool + negotiation + gzip**
  - Acceptance: 4 workers + shuffled delivery byte-exact under `-count=20`;
    eager flush preserved at conc=4; coalescing under saturation preserved;
    gzip only-if-smaller; hung-POST Close doesn't hang; goroutines settle;
    batcher.go byte-identical
  - Verify: `go test ./internal/transport/ -race -count=20`
  - Files: `internal/transport/caps.go`, `internal/transport/caps_test.go`,
    `internal/transport/conn.go`, `internal/transport/conn_test.go`

- [x] **Step 5: Compat matrix part 2 + gzip quadrants**
  - Acceptance: full-caps agent × stripped server → serial 16KiB fallback,
    byte-exact, bodies ≤16KiB; gzip agent × stripped server → raw, no flag
  - Verify: `go test ./... -race -count=1`
  - Files: `internal/server/compat_test.go`

- [x] **Step 6: Probe + /probe endpoint + middlebox throttles**
  - Acceptance: probe detects 64KiB cliff within one step and classifies
    per-conn vs aggregate throttle correctly against the middlebox;
    /probe registers no session, enforces cap
  - Verify: `go test ./internal/probe/ ./internal/server/ -race`
  - Files: `internal/probe/probe.go`, `internal/probe/probe_test.go`,
    `internal/server/handlers.go`, `internal/server/server_test.go`,
    `internal/testutil/middlebox.go`

- [x] **Step 7: CLI flags + concurrent e2e**
  - Acceptance: --batch-size/--concurrency/--compress clamp correctly;
    probe subcommand prints report; e2e 1MiB byte-exact at 4/64KiB/gzip
  - Verify: `go build ./... && go test ./internal/server/ -run E2E -race`
  - Files: `cmd/ssetunnel/main.go`, `internal/agent/agent.go`,
    `internal/server/e2e_test.go`

- [x] **Step 8: Bench — upstream budget + gzip proof + regression**
  - Acceptance: upstream ≥4MB/s (4/64KiB, 10ms middlebox) with serial
    control ratio printed; compressible wire ≤½ payload, incompressible
    ≤1% overhead; ALL cycle-1 budgets re-run unmodified and PASS
  - Verify: `go test ./internal/transport/ -run Bench -v -timeout 10m` (manual)
  - Files: `internal/transport/bench_test.go`
