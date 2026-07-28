# Spec: ssetunnel — Cycle 2 (Upstream Throughput)

> Base spec: `docs/specs/ssetunnel.md` (style, boundaries, testing rules
> apply unchanged). Idea: `docs/ideas/ssetunnel-cycle2.md`.
> Delivery: split — this cycle is throughput only. Auth + console +
> `connect` wrapper are cycle 3 (separate spec).

## Objective

The real-proxy spike confirmed the SSE downstream is healthy and the
serial-POST upstream is the throughput bottleneck (cycle-1 ceiling:
`16KiB / POST-RTT`). Raise agent→server throughput as high as the proxy
allows, without tripping the proxy behaviors (body-size caps, DLP
buffering) that forced discrete POSTs in the first place.

**Users:** the same small team. The immediate beneficiary is anyone
pushing data *into* the tunnel (git push, scp upload, DB import) — today
that direction is ~10× slower than the downstream.

### Acceptance criteria (functional)

- `ssetunnel probe --server URL` runs against a live server and reports:
  POST body-size cliff (escalating sizes), RTT vs body size (DLP scan
  latency), and per-connection vs aggregate throttling (parallel streams).
  Output is a plain-text report ending in a recommendation (batch size,
  concurrency depth, or "aggregate cap — concurrency won't help").
- The agent negotiates capabilities with the server on connect
  (`/events` response header) and enables only what both sides support:
  concurrent POSTs (2–4), larger batches (up to probed/configured
  ceiling), gzip. A cycle-1 agent works against a cycle-2 server and
  vice versa (capability absent → serial 16KiB behavior, exactly as
  cycle 1).
- Server reassembles concurrent POSTs via a reorder window keyed on the
  existing `X-SSET-Seq`: in-order passthrough, out-of-order buffering,
  duplicate dedup, gap timeout → session death (fail-fast model
  unchanged).
- Optional gzip per batch, flagged by an upstream header, applied only
  when the compressed payload is smaller; negotiated via capabilities.
- Probe conclusions are consumable by the agent: `--batch-size` and
  `--concurrency` flags (defaults 16KiB / 1; maximums 1MiB / 4).

## Tech Stack

- Go 1.22+, module `github.com/wseternal/ssetunnel` — unchanged
- **No new dependencies.** gzip is stdlib (`compress/gzip`). yamux
  config unchanged.
- Wire format changes are additive headers only (`X-SSET-Caps`,
  `X-SSET-Flags`); no change to the seq/session header contract.

## Commands

```bash
go build ./...                          # build
go test ./... -race -count=1            # unit + integration gate
go vet ./...                            # lint baseline

# Probe a live server (through the real proxy when deployed)
./ssetunnel probe --server https://tunnel.example.com

# Agent with probe-informed settings
./ssetunnel agent --server URL --target ADDR \
    --batch-size 65536 --concurrency 4 --compress

# Bench (manual, includes new upstream-throughput measurement)
go test ./internal/transport/ -run Bench -v -timeout 10m
```

## Project Structure (deltas only)

```
internal/transport/
  reorder.go          → NEW: server-side reorder window (pure core)
  batcher.go          → MOD: configurable maxSize (adaptive ceiling)
  conn.go             → MOD: N concurrent POST senders, capability
                        negotiation, gzip-on-smaller, new flags
internal/server/
  session.go          → MOD: reorder window replaces monotonic assert
                        (window size 1 = cycle-1 behavior)
  handlers.go         → MOD: X-SSET-Caps advertisement, X-SSET-Flags
                        (gzip) handling
internal/probe/       → NEW: probe logic (size escalation, RTT-vs-size,
                        parallel-stream throttle test, report)
cmd/ssetunnel/main.go → MOD: probe subcommand, new agent flags
internal/transport/bench_test.go → MOD: upstream-throughput budget
```

## Code Style

Per base spec. Reminders that bite in this cycle: reorder window core is
a pure function (`Push(seq, payload) → (ready [][]byte, err)`) with no
goroutines inside; all intervals/sizes are struct fields; capability
negotiation fails closed (absent header → cycle-1 behavior).

## Testing Strategy

Per base spec (stdlib, `-race` gate, count-based timing), plus:

- **Reorder window:** table-driven property tests — every permutation of
  a shuffled 8-seq window reassembles correctly; duplicates dropped;
  window-full error; gap-timeout error (shortened timeout).
- **Concurrency:** agent with 4 senders against real handlers through
  `httptest`; byte-exact reassembly under artificial response shuffling
  (server test hook that delays chosen seqs).
- **Negotiation:** cycle-1-mode agent (no caps) ↔ cycle-2 server and
  cycle-2 agent ↔ caps-stripped server both fall back to serial 16KiB
  with byte-exact echo.
- **gzip:** compressible payload round-trips byte-exact; incompressible
  payload is sent raw (flag absent) — asserted via body inspection.
- **Probe:** integration test against a middlebox configured with a
  known body cap and known per-conn throttle — probe report must detect
  both correctly.
- **Bench:** new upstream-throughput measurement (target→entry
  direction) through the latency-injecting middlebox; all cycle-1
  budgets re-run unchanged.

## Boundaries

- **Always:** `go test ./... -race -count=1` green per task; additive
  wire changes only; fail closed on missing capabilities
- **Ask first:** any new dependency (none expected); changing the
  seq/session header contract; making gzip or concurrency unconditional
  (they stay negotiated/opt-in)
- **Never:** streaming request bodies; retransmission/NACK protocol;
  weakening the 413 body-cap compliance; removing cycle-1 fallback
  behavior

## Success Criteria

Measured through the middlebox with 10ms injected latency per direction
(POST RTT ≈ 20ms), compared against the cycle-1 serial baseline
(~0.8 MB/s upstream):

- **Upstream throughput:** ≥ 4 MB/s target→entry through the harness
  with `--concurrency 4 --batch-size 65536` (≈5× baseline); cycle-1
  budgets (p50 added latency ≤50ms, downstream ≥5 MB/s, 32 streams no
  HoL, reconnect <5s, zero leaks) all still PASS
- **Reorder correctness:** property tests + shuffled-delivery
  integration test green under `-race -count=20`
- **Probe accuracy:** against a middlebox with 64KiB cap and 2-stream
  per-conn throttle, the report identifies the cliff within one
  escalation step and correctly classifies per-conn vs aggregate
- **Backward compatibility:** mixed-version matrix (old agent/new
  server, new agent/old server) echoes byte-exact in cycle-1 mode
- **Compression:** compressible upstream payload shows ≥2× wire-bytes
  reduction in the bench; incompressible payload pays ≤1% overhead

Done = all gates green, bench shows the upstream number, probe run
against the middlebox demo'd, and the real-proxy re-spike (manual)
confirms the gain in the actual environment.

## Open Questions

- None blocking. Probe's real-proxy results finalize the shipped
  defaults (batch ceiling, concurrency depth).
- (Resolved: delivery = split, cycle 2 is throughput only; `connect`
  wrapper ships with auth in cycle 3; user handshake will be
  token-first-frame — all cycle-3 spec material.)
