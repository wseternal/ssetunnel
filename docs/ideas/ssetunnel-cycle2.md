# ssetunnel — Cycle 2: Upstream Throughput, Auth, Console

## Problem Statement
How might we maximize agent→server throughput through the corporate proxy
(SSE path proven healthy, POST path confirmed capped), while making the
tunnel safe to expose — enrolled access via TOTP or PIN — and manageable
through an embedded web console?

## Recommended Direction
Probe-first, then both levers. New `ssetunnel probe` subcommand measures
the proxy's POST behavior (body-size cliff via escalating sizes,
per-connection vs aggregate throttling via parallel streams, RTT-vs-size
for DLP scan latency). Transport then gains: adaptive batch sizing
(16KiB start, grows to probed ceiling), 2–4 concurrent POSTs with the
deferred server-side reorder window (pure-function core, seq header
already on the wire), and optional per-batch gzip (stdlib, sent only
when smaller). Wire format unchanged.

Auth: POST /register accepts either a TOTP code (shared enrollment
secret) or a single-use PIN (8+ base32 chars, 15min expiry, generated
in the console) → bearer token (role agent|user, JSON store, 0600,
atomic writes). Console: React+Vite SPA embedded via go:embed —
TOTP-login admin session (12h cookie), enrollment (TOTP QR + PIN
generation), token/user/agent management, live session status.
Served from the same HTTP server as the management JSON API.

## Key Assumptions to Validate
- [ ] Throttle is per-connection or RTT-serialization, not aggregate —
      probe's parallel-stream test decides; if aggregate, accept the cap
      and skip concurrency (document why)
- [ ] Body-size cliff exists above 16KiB or doesn't — probe's escalation
      test finds it; adaptive sizing starts conservative regardless
- [ ] DLP scan latency doesn't eat the big-batch gain — probe measures
      RTT vs body size
- [ ] PIN entropy (40 bits, single-use, 15min) is sufficient without
      rate limiting — accepted risk per environment confirmation

## MVP Scope
In:  probe subcommand; adaptive batch + 2-4 concurrent POSTs + reorder
     window + optional gzip; /register with TOTP and PIN paths; bearer
     token store + middleware on all endpoints; TLS-or-allow-insecure
     unchanged; embedded console (login, enrollment, tokens, agents,
     session status); bench updated to prove upstream improvement
     through the middlebox.
Out: multi-target routing, audit log, session resumption, rate limiting,
     agent-side entry, console metrics/graphs.

## Not Doing (and Why)
- Parallel yamux session sharding — multiplies SSE connections and
  bookkeeping; complexity moved, not removed
- Direct-to-server bulk upload bypass — same proxy path, same cap
- New compression dependency — stdlib gzip is sufficient
- Rate limiting on PIN/login — environment confirmed not a concern;
  PIN entropy + single-use + expiry carries the risk
- Console dashboards/metrics graphs — status table only; same refusal
  as cycle 1's "console extras"

## Open Questions
- None blocking. Probe results will finalize: batch ceiling, concurrency
  depth (2 vs 4), and whether concurrency ships at all.
- Delivery shape: one umbrella cycle with three sequential workstreams
  (transport → auth → console), or split into cycle 2 (throughput) and
  cycle 3 (auth + console) — decide at planning time.
