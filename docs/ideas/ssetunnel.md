# ssetunnel — SSE Reverse TCP Tunnel

## Problem Statement
How might we expose private TCP services through a public server when the
agent side sits behind a restrictive corporate proxy that only allows
plain outbound HTTP(S) request/response — no WebSocket, no raw TCP,
and no long-lived request bodies?

## Recommended Direction
One Go binary, three subcommands: server, agent, connect (client wrapper).

Transport: net.Conn adapter over two asymmetric HTTP channels, shaped by
corporate-proxy behavior:
- Downstream: single long-lived SSE GET, base64 payloads, 15s keep-alive
  comments, X-Accel-Buffering: no.
- Upstream: discrete short POSTs, batched at 16KB/25ms, binary bodies,
  sequence-numbered, reassembled server-side through a reorder window,
  2-4 concurrent in flight. No streaming request bodies — DLP proxies
  buffer or kill them.
Yamux over the adapter provides multiplexing, flow control, keepalive.

Auth is enroll-once: TOTP code exchanged at POST /register for a
long-lived bearer token (role agent|user). Enrollment secrets, token
issuance/revocation, and agent/session status are managed through an
embedded web console — React/Vite SPA built to frontend/console/dist,
embedded via go:embed, served as a catch-all route alongside the JSON
management API (pattern from auth-go's consoleserver + litespaserver;
reimplement the embedded-SPA handler if orcacommon isn't importable).
No CLI management surface beyond the three subcommands.

TLS: if the server environment provides cert/key, use them (entry
listener + HTTP server); otherwise an --allow-insecure flag runs plain
HTTP/TCP explicitly. No self-signed machinery, no ACME.

## Key Assumptions to Validate
- [ ] SSE GET survives the real proxy for hours with 15s heartbeats —
      spike test: hold a stream 10+ min, watch for FIN/RST
- [ ] Batch concurrency (2-4 in flight) meets target workloads —
      benchmark throughput through the real proxy
- [ ] Session-loss-on-drop is acceptable UX; agent auto-reconnect +
      user retry is sufficient
- [ ] litespaserver is importable, or the ~40-line embedded-SPA
      replacement is acceptable

## MVP Scope
In:  SSE+POST net.Conn adapter (batching, seq numbers, reorder window),
     yamux, SSE heartbeat filter, TOTP enrollment, bearer tokens (JSON
     store), TLS-or-allow-insecure listeners, agent auto-reconnect,
     connect wrapper that injects tokens (ProxyCommand-compatible),
     embedded management console (agents, users/tokens, session status)
     with its JSON API.
Out: multi-target routing (leave `target` field in protocol), audit log,
     subdomains, agent-side entry, session resumption, ACME/self-signed
     cert generation, rate limiting.

## Not Doing (and Why)
- Streaming/chunked POST request bodies — DLP store-and-forward buffers
  them; hard 2-5 min request-body timeouts kill them. Rejected on
  corporate-proxy evidence.
- Rate limiting / POST-throttling defenses — confirmed not a concern in
  the target environment
- Self-signed certs or ACME — env-provided TLS or explicit
  --allow-insecure; keep it simple
- CLI management surface — all management lives in the web console
- Per-connection OTP — enrollment-only; daily friction kills adoption
- Agent-side entry point — explicitly declined
- Session resumption after drop — reconnect+retry is sufficient
- Custom mux protocol — yamux already solved flow control and half-close
- Console extras (dashboards, metrics graphs, audit viewer) — console
  covers enrollment + tokens + status only

## Open Questions
- None blocking. (Resolved: rate limits not a concern; TLS = env-provided
  or allow-insecure; management = embedded web console.)
