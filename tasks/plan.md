# Plan: ssetunnel — Cycle 2 (Upstream Throughput)

> Spec: `docs/specs/ssetunnel-cycle2.md` · Idea: `docs/ideas/ssetunnel-cycle2.md`
> Supersedes cycle-1 plan (in git history at 8b004a0:tasks/plan.md).
> Synthesized from 3 parallel planning perspectives (simplicity / performance / minimal-risk).

## Goal

Raise agent→server throughput from the cycle-1 serial ceiling
(`16KiB / POST-RTT` ≈ 0.8 MB/s at harness RTT) to ≥4 MB/s through the
latency-injecting middlebox, via negotiated concurrent POSTs + reorder
window + larger batches + optional gzip — while every cycle-1 budget and
behavior stays provably intact.

## Key Design Decisions

1. **Batcher untouched.** The worker pool lives between batcher flush
   and the HTTP POST, inside `conn.go`. When the pool's bounded channel
   is full, flush blocks → batcher's `busy` flag still means "saturated"
   → eager flush and 25ms coalescing behave exactly as tested in cycle 1.
   Cost: flush errors become asynchronous → `Conn` gains a sticky
   `sendErr` checked by `Write` alongside `batcher.Err()`. N=1 path is
   line-for-line identical to cycle 1.
2. **Seq at submit, never in workers.** `submit` is called solely by the
   batcher's single run goroutine and assigns `c.seq.Add(1)-1` there;
   workers receive `{seq, body}` pairs. Seq order == byte order by
   construction; the silent-corruption race class is designed out.
3. **Two-way capability negotiation, fail closed per axis.**
   Server advertises `X-SSET-Caps: concurrency=4;batch=1048576;gzip` on
   the `/events` 200 response (consts, not config). Agent parses
   (absent/malformed → cycle-1 defaults, never a dial error), intersects
   with its flags, and echoes its *chosen* set on the `/events` request.
   Server builds a reorder window only when the agent negotiated
   concurrency>1; otherwise the cycle-1 `push` path runs verbatim.
4. **Reorder window: pure core, map-based, 8 slots fixed.**
   `Push(seq, payload) → (ready [][]byte, err)`; no goroutines, no
   timers. Gap timeout (default 25s, struct field, injected `now` in
   tests) is checked piggyback on each `Push` — yamux keepalive
   guarantees upstream traffic within 30s, so a silent gap is still
   detected. Duplicates (`seq < base`) → dropped, 200. Window-full or
   gap-timeout → 409 → session death (fail-fast model unchanged).
   Memory bound: 8 slots × 1 MiB = 8 MiB worst case.
5. **gzip at the conn layer.** `gzip.BestSpeed` (~0.3ms/64KiB batch,
   parallelized across workers); sent only when compressed < raw
   (incompressible pays only the attempt, ≤1% overhead by construction);
   flagged `X-SSET-Flags: gzip`. Server: unknown flag value → 400;
   gzip flag on a non-negotiated session → 400 (unreachable under
   correct negotiation, fail closed).
6. **`POST /probe` endpoint (additive).** Read-and-discard body, 2 MiB
   cap, 200, no session registration. Required because probing through
   `/events` hijacks the live agent's yamux session (`server.go:43`
   unconditional attach) and `/up` feeds junk into the mux. Approved
   deviation from the spec's structure section.
7. **Dial-time batch configuration.** "Adaptive" = probe informs flags,
   negotiation clamps the ceiling (`min(flag, server-advertised)`),
   `maxSize` immutable after construction. Runtime growth rejected:
   413 = session death, so no failure signal exists to adapt to.
   Approved deviation from the idea doc's wording; spec already matches.
8. **`maxUpBody` raised to 1 MiB + 64 KiB slack** — the batch ceiling
   must never equal the body cap (off-by-exactly-equal → 413 → session
   death). gzip only ever shrinks bodies.
9. **HTTP client retune:** `MaxIdleConnsPerHost = 8` (4 POST + 1 SSE +
   reconnect overlap + spare); `MaxConnsPerHost` deliberately unset
   (senders self-limit; a cap could starve the SSE GET).
10. **Mixed-version matrix via `StripHeaders`** middlebox knob (test
    code; also models realistic header-stripping proxies) and a
    test-only `Config.DisableCaps`. No build tags, no env vars.

## Steps (TDD per step: RED → GREEN → `go test ./... -race -count=1 && go vet ./...`)

### Step 1 — Reorder window pure core
- `internal/transport/reorder.go` per decision 4; sentinels
  `ErrWindowFull`, `ErrGapTimeout`.
- Tests: all 8! permutations of a shuffled 8-seq window reassemble
  byte-exact; duplicates dropped; window-full; gap timeout via injected
  `now`; in-order passthrough.
- Verify: `go test ./internal/transport/ -run TestReorder -race -count=20`
- Files: `internal/transport/reorder.go`, `internal/transport/reorder_test.go`

### Step 2 — Server: window-gated push, caps advertisement, gzip decode
- `Session.push`: window path when agent negotiated concurrency, legacy
  path verbatim otherwise; gap timeout kills session. `handleEvents`:
  response caps advertisement + request caps parsing. `handleUp`:
  `X-SSET-Flags: gzip` decode (400 on unknown flag or non-negotiated
  gzip); `maxUpBody` slack per decision 8.
- Tests: shuffled POSTs via deterministic release-gate hook (channels,
  no sleeps) reassemble byte-exact; legacy-path tests unmodified green;
  caps header well-formed; gzip round-trip; 400 cases; 1 MiB exact-boundary
  accepted.
- Verify: `go test ./internal/server/ -race -count=5`
- Files: `internal/server/session.go`, `internal/server/handlers.go`,
  `internal/server/server_test.go`

### Step 3 — Compat matrix part 1: old agent × new server (EARLY)
- `MiddleboxConfig.StripHeaders` knob; `Config.DisableCaps` test knob.
- Tests: cycle-1-mode agent (no caps) × cycle-2 server advertising full
  caps: byte-exact 1 MiB echo both directions; server takes the legacy
  no-window path for this session.
- Verify: `go test ./internal/server/ -run TestCompat -race`
- Files: `internal/testutil/middlebox.go`, `internal/server/compat_test.go`

### Step 4 — Agent: sender pool, seq-at-submit, negotiation, gzip
- `caps.go`: `Caps`, `ParseCaps`, `NegotiateCaps` (fail closed inside
  parse). `conn.go`: `Config.Concurrency`/`Compress`; pool per decision
  1–2 when negotiated conc>1; gzip per decision 5; transport retune per
  decision 9; `Close` handles hung POST at conc=4.
- Tests: 4 workers + server-side shuffling → byte-exact under
  `-count=20`; eager flush preserved at conc=4 (idle pool → immediate
  flush); coalescing under saturation preserved (full pool → batches);
  gzip sent-only-if-smaller (body inspection); `Close` with hung POST
  doesn't hang; goroutines settle.
- Verify: `go test ./internal/transport/ -race -count=20`
- Files: `internal/transport/caps.go`, `internal/transport/caps_test.go`,
  `internal/transport/conn.go`, `internal/transport/conn_test.go`

### Step 5 — Compat matrix part 2 + gzip quadrants
- Tests: full-caps agent × caps-stripped server → serial 16KiB fallback,
  byte-exact, all bodies ≤16KiB; gzip agent × stripped server → raw
  bodies, no flag.
- Verify: `go test ./... -race -count=1`
- Files: `internal/server/compat_test.go` (+ conn tests as needed)

### Step 6 — Probe package + /probe endpoint + middlebox throttles
- `POST /probe` per decision 6. Middlebox: `PerConnRate`/`GlobalRate`
  throttle knobs + `PostBytes` wire counter. `internal/probe`: size
  escalation (16KiB→1MiB doublings) → cliff within one step; RTT-vs-size
  table; 1-vs-4 parallel → per-conn vs aggregate classification;
  plain-text report ending in a recommendation.
- Tests: middlebox with 64KiB cap + 2-stream per-conn throttle → report
  detects both; global throttle → classified aggregate; `/probe` handler
  unit test (no session registered, cap enforced).
- Verify: `go test ./internal/probe/ ./internal/server/ -race`
- Files: `internal/probe/probe.go`, `internal/probe/probe_test.go`,
  `internal/server/handlers.go`, `internal/server/server_test.go`,
  `internal/testutil/middlebox.go`

### Step 7 — CLI flags + concurrent e2e
- `agent --batch-size` (16KiB default, clamp 1 MiB), `--concurrency`
  (1 default, clamp 4), `--compress`; `probe --server URL` subcommand.
- Tests: flag clamping; e2e byte-exact 1 MiB echo with agent at
  4/64KiB/gzip through the real server+entry path.
- Verify: `go build ./... && go test ./internal/server/ -run E2E -race`
- Files: `cmd/ssetunnel/main.go`, `internal/agent/agent.go`,
  `internal/server/e2e_test.go`

### Step 8 — Bench: upstream budget + gzip proof + cycle-1 regression
- Blast-mode target ('U': floods on connect); measure target→entry with
  4/64KiB through the 10ms middlebox; serial-16KiB control prints the
  ratio. gzip bench: compressible → wire bytes ≤½ payload (PostBytes
  counter); incompressible → ≤1% overhead. All cycle-1 benches re-run
  UNMODIFIED (default serial agent = regression proof).
- Expected: ~12 MB/s theoretical (4×64KiB/20ms) vs ≥4 MB/s budget → 3×
  margin, non-flaky.
- Verify: `go test ./internal/transport/ -run Bench -v -timeout 10m`
- Files: `internal/transport/bench_test.go`

## Dependencies

```
1 → 2 → 3 → 4 → 5 → 7 → 8
    2 → 6 ────────┘        (6 parallel with 4-5 once step 2 lands)
```

## Rejected Alternatives

- **Modifying the batcher** (busy counter, seq-at-enqueue — Plans B/C):
  touches the cycle's most race-sensitive tested component for no
  behavioral gain; the bounded pool channel gives the same backpressure
  semantics with batcher.go byte-identical.
- **Partitioned seq ranges per sender** (Plan B considered): a stalled
  sender holds its range hostage; shared seq at the single submit point
  is strictly simpler and fairer across yamux streams.
- **Timer-goroutine gap timeout** (Plan C's AfterFunc): piggybacked
  check on Push needs no goroutine; yamux keepalive (30s) bounds
  detection latency.
- **Runtime-adaptive batch sizing** (idea doc wording): learns the
  ceiling by killing sessions (413 = death); probe + flags + negotiation
  provide the same outcome with zero mid-stream mutation races.
- **Probing via /events+/up**: provably hijacks the live agent's session
  (unconditional attach at server.go:43) and can't distinguish server
  413 from proxy 413. `/probe` endpoint added instead.
- **Raising the bench middlebox's 64KiB cap**: shipped bench config is
  64KiB batches; the exact-fit cap actively validates 413 compliance.

## Risks Carried

- Simulated throttle ≠ real DLP; the probe's real-proxy run finalizes
  shipped defaults (spec open question).
- Ack-on-buffer means a straggler POST can fill the window and kill the
  session — accepted fail-fast behavior; window 8 ≈ 2 RTTs tolerance,
  25s backstop, <5s reconnect budget covers it.
- `/probe` is unauthenticated surface until cycle 3 (bounded: discards
  bodies, 2 MiB cap).
