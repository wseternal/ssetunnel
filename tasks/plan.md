# Plan: ssetunnel — Cycle 1 (Transport Core)

> Spec: `docs/specs/ssetunnel.md` · Idea: `docs/ideas/ssetunnel.md`
> Synthesized from 3 parallel planning perspectives (simplicity / performance / minimal-risk).

## Goal

Prove the project's core assumption end-to-end: **yamux multiplexing over an
SSE-down + batched-POST-up transport survives a hostile middlebox** (idle
killer, body cap, injected latency) and meets the spec's performance budgets.

Cycle 1 delivers: transport adapter, yamux wiring, minimal server + agent,
middlebox simulation, bench harness, working `server`/`agent` binaries.
No auth, no TLS, no console, no `connect` wrapper (cycles 2+).

## Key Design Decisions

1. **Serial upstream POSTs in cycle 1.** One POST in flight → ordering
   correct by construction; reorder window and its entire race class deleted.
   `X-SSET-Seq` header is on the wire from day one (server asserts
   monotonic seq, 409 on gap) so adding 2–4 concurrent POSTs + reorder
   window in cycle 2 is a server+sender change with **no protocol change**.
   Gated on: bench numbers + real-proxy spike evidence.
2. **Eager-flush batcher.** Flush immediately when the sender is idle;
   the 16 KiB / 25 ms ceiling only applies under saturation. Near-zero
   added latency when idle (interactive-first profile), full batches under
   bulk load. Spec's `Batcher` sketch is the contract.
3. **Fail-fast gap policy.** A lost POST = seq gap = 409 = session death →
   agent reconnects (<5s budget). No retransmit, no NACK (spec "Not
   Doing"). Server dedups old seqs so a future retry is idempotent-safe.
4. **Wire format.** Upstream: `POST /up` with `X-SSET-Session`, `X-SSET-Seq`
   headers, raw binary body ≤16 KiB. Downstream: SSE `data: <base64>\n\n`
   frames, `: ka\n\n` comment every 15s, `X-Accel-Buffering: no`.
5. **Session registry.** Agent generates 128-bit random session ID per
   connect; server keeps `map[string]*Session` (mutex). New ID replaces
   stale session. No package-level state (spec anti-pattern).
6. **yamux config:** `MaxStreamWindowSize = 1 MiB` (256 KiB ÷ 100ms
   effective RTT = 2.5 MB/s < budget), `KeepAliveInterval = 30s`,
   `AcceptBacklog = 256`. Each value justified in a comment.
7. **HTTP plumbing gotchas (must-haves):** server `WriteTimeout = 0`
   (else SSE dies), only `ReadHeaderTimeout` set; client transport with
   explicit `MaxIdleConnsPerHost` and compression disabled; response
   bodies always drained.
8. **Deadlines implemented honestly.** Read deadline via mutex-guarded
   timer + select; write deadline maps to the POST request context. No
   silent no-ops (~40 lines).
9. **Goroutine ownership:** every goroutine hangs off a context owned by
   its conn; `Close` = `sync.Once` + cancel + `io.Pipe.CloseWithError` +
   `WaitGroup` wait. Leak checks via `runtime.NumGoroutine` settle-poll
   (dep freeze: no goleak).
10. **Tests are count-based, not timing-based.** Intervals are struct
    fields; tests use tiny intervals and loose bounds (100× margin) or
    explicit flush hooks. The real 15s/60s timings live in the bench.

## Architecture (cycle 1 packages)

```
cmd/ssetunnel/main.go     subcommand dispatch: server | agent
internal/transport/
  sse.go                  SSE codec (encode/decode, heartbeat filter) — pure
  batcher.go              eager-flush batcher → batch channel → sender goroutine
  conn.go                 agent-side net.Conn (owns both goroutines, deadlines)
  bench_test.go           budget harness (manual run, testing.Short-gated)
internal/mux/mux.go       yamux Server()/Client() + config
internal/server/
  session.go              Session + Registry (map[id]*Session)
  handlers.go             GET /events, POST /up
  server.go               HTTP server + TCP entry listener wiring
  e2e_test.go             end-to-end echo
internal/agent/agent.go   dial loop: connect → yamux client → per-stream
                          target dial; auto-reconnect with backoff
internal/testutil/
  middlebox.go            idle-killer + body-cap + latency-injection proxy
```

(Note: spec's tree put `/events`+`/up` mechanics under `internal/server` —
followed literally; `internal/transport` holds the wire/codec/conn logic.)

## Implementation Steps

TDD per step: failing test first (RED), minimal implementation (GREEN),
refactor, `go test ./... -race -count=1` + `go vet ./...` before moving on.

### Step 1 — Module scaffold
- `go.mod` (module `github.com/wseternal/ssetunnel`, go 1.22), yamux dep,
  `cmd/ssetunnel/main.go` dispatch stub, package doc comments.
- Accept: `go build ./... && go vet ./...` clean.
- Files: `go.mod`, `cmd/ssetunnel/main.go` (+ doc.go per package as created)

### Step 2 — SSE codec (pure, no goroutines)
- `writeFrame(w, flusher, payload)`, heartbeat comment writer, incremental
  line decoder: split-line reassembly, comment filtering, multi-frame,
  oversized-line guard.
- Tests: round-trip incl. binary; heartbeats never surface as data;
  flush-per-frame asserted with counting Flusher.
- Verify: `go test ./internal/transport/ -run TestSSE -race`
- Files: `internal/transport/sse.go`, `internal/transport/sse_test.go`

### Step 3 — Eager-flush batcher
- Mutex + buffer swap; size flush at maxSize; 25ms `AfterFunc` armed only
  when sender busy; eager flush when sender idle; `Close` drains + waits.
- Tests (count-based): boundary at maxSize, no empty/double flush under
  `-race` hammer test, timer path with 100× margin, Close drains.
- Verify: `go test ./internal/transport/ -run TestBatch -race`
- Files: `internal/transport/batcher.go`, `internal/transport/batcher_test.go`

### Step 4 — Server session, registry, handlers
- `Session`: id, server-side `net.Conn` (io.Pipe pair + deadline logic),
  last-seq state. `Registry`: mutex-guarded map, replace-on-new-ID.
- Handlers: `GET /events?id=` streams SSE from pipe; `POST /up` validates
  session + monotonic seq (409 on gap/unknown), feeds pipe.
- Tests (httptest): POST→Read in order; Write→SSE frames; 409 on gap and
  unknown ID; session replacement; Read gets EOF on session close;
  read-deadline expiry returns `i/o timeout`.
- Verify: `go test ./internal/server/ -race`
- Files: `internal/server/session.go`, `internal/server/handlers.go`,
  `internal/server/server_test.go`

### Step 5 — Agent-side net.Conn
- `DialAgent(ctx, cfg)`: SSE GET goroutine (heartbeat filter) → Read;
  Write → batcher → single POST sender goroutine (serial); deadlines per
  decision 8; Close per decision 9.
- Tests against real Step-4 handlers via `httptest.NewServer`: echo both
  directions; batching observed (POST count < write count); close with
  unread buffered data doesn't hang (timeout-guarded); goroutine count
  settles after Close; concurrent Writes from 8 goroutines under `-race`;
  POST 5xx → Write error + conn close.
- Verify: `go test ./internal/transport/ -run TestConn -race`
- Files: `internal/transport/conn.go`, `internal/transport/conn_test.go`

### Step 6 — yamux mux
- `mux.Server(conn)` / `mux.Client(conn)` with config per decision 6.
- Tests over the real adapter: session establishment; echo across a
  stream; 32 concurrent streams with one stalled reader — others complete
  (head-of-line test); >256 KiB transfer without reads (window proof).
- Verify: `go test ./internal/mux/ -race`
- Files: `internal/mux/mux.go`, `internal/mux/mux_test.go`

### Step 7 — Server/agent wiring + e2e echo + binaries
- `internal/server/server.go`: HTTP server (`WriteTimeout: 0`,
  `ReadHeaderTimeout: 10s`) + TCP entry listener (one yamux stream per
  accepted conn, bidirectional copy).
- `internal/agent/agent.go`: connect → yamux client → `AcceptStream` loop
  → dial target → proxy; reconnect backoff (immediate first retry, cap 1s).
- `main.go`: `server --listen :8080 --entry :9090`,
  `agent --server URL --target ADDR` via stdlib `flag`.
- e2e test (real TCP, in-process): byte-exact 1 MiB patterned echo through
  entry→tunnel→target; kill SSE mid-test → agent reconnects <5s, entry
  side gets clean error (no hang); two concurrent connections.
- Verify: `go test ./... -race`; `go build ./cmd/ssetunnel`; manual smoke
  (`nc` to entry port echoes).
- Files: `internal/server/server.go`, `internal/agent/agent.go`,
  `internal/server/e2e_test.go`, `cmd/ssetunnel/main.go`

### Step 8 — Middlebox
- `testutil.Middlebox`: HTTP reverse proxy with (a) idle-timeout killer
  (hijack + close), (b) POST body cap → 413, (c) per-direction latency
  injection. All parameterized.
- Tests: SSE survives 3× idle-kill interval at 4:1 heartbeat ratio
  (e.g. 75ms heartbeats vs 300ms killer); control case with heartbeats
  off dies at timeout; bulk transfer never trips body cap (zero 413s);
  reconnect after killed SSE succeeds.
- Verify: `go test ./... -run Middlebox -race`
- Files: `internal/testutil/middlebox.go`,
  `internal/server/middlebox_test.go`

### Step 9 — Bench harness (the budget proof)
- `internal/transport/bench_test.go`, skipped under `testing.Short()`,
  run manually with generous timeout. All measurements through the
  middlebox with 10ms injected latency per direction (otherwise loopback
  floor makes deltas meaningless):
  1. **Added latency:** 64-byte ping-pong ×2000, p50/p95 of
     (tunnel − direct). Budget: p50 ≤ 50ms.
  2. **Throughput:** 256 MiB single stream, wall-clock. Budget ≥5 MB/s.
     Print POST count per MiB (batching efficiency visibility).
  3. **Concurrency:** 32 streams, one stalled after 64 KiB; others
     complete 1 MiB each within deadline; wall time ≈ single-stream.
  4. **Reconnect:** kill SSE 100×; each re-establishment <5s; goroutine
     count + heap settle ±10% (cycle 100 vs cycle 5).
- Accept: all four budgets printed with measured values + PASS/FAIL.
- Verify: `go test ./internal/transport/ -run Bench -v -timeout 10m`
- Files: `internal/transport/bench_test.go`

## Dependencies

```
1 → 2 ─┐
1 → 3 ─┼→ 4 → 5 → 6 → 7 → 8 → 9
```
Steps 2–3 parallelizable; 4 needs 2; 5 needs 2+3+4; 6 needs 5; 8's helper
is standalone but its tests need 7; 9 needs everything.

## Rejected Alternatives

- **4 concurrent POSTs + reorder window in C1** (Plan B): highest
  race-density component; on the loopback bench serial POSTs already
  exceed the throughput budget, and at real-proxy RTTs even 4×
  concurrency fails per B's own math — so concurrency doesn't decide
  any C1 budget. Deferred to cycle 2, gated on evidence; wire format
  already carries the seq header.
- **Building the reorder window "for later" anyway** (Plan A): speculative
  code that can't be exercised by the serial sender is untested code.
  Seq-monotonicity assertion in C1 provides the reorder-detection value.
- **Always-wait 25ms batching** (naive reading of spec sketch): adds
  12.5ms avg latency to every interactive round-trip for zero benefit
  when the pipeline is idle. Eager flush keeps the 25ms ceiling as a
  saturation-only behavior.
- **Client-side same-seq POST retry** (Plan B): dedup is built so retry
  is *safe*, but with serial POSTs a failed POST already means session
  death + <5s reconnect. Retry adds agent complexity to save a reconnect
  the budget already tolerates. Revisit with concurrency in cycle 2.
- **256 KiB yamux window** (default): 2.5 MB/s ceiling at 100ms effective
  RTT — fails the throughput budget at hostile RTTs. 1 MiB chosen.
- **goleak / testify / fake-clock deps**: spec dep freeze. Stdlib
  `runtime.NumGoroutine` settle-poll and count-based tests instead.

## Risks Carried Into Cycle 1

- Simulated middlebox ≠ real DLP proxy. The real-proxy SSE spike
  (10+ min hold) remains a manual pre-release activity.
- Serial-POST throughput through the real proxy is unproven — this is
  the explicit gate for cycle 2's concurrency work.
- Cycle 1 has no auth: 128-bit session IDs make casual hijack
  infeasible; binaries are dev-only until cycle 2/3 land.
