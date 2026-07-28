# Spec: ssetunnel — SSE Reverse TCP Tunnel

> Source idea: `docs/ideas/ssetunnel.md` (refined and reviewed).

## Objective

Expose private TCP services through a public server when the agent side
sits behind a restrictive corporate proxy that allows only plain outbound
HTTP(S) request/response — no WebSocket, no raw TCP, no long-lived
request bodies.

**Users:** a small team. Admins manage agents/users through an embedded
web console; agents run inside restricted networks; users connect through
a TLS entry listener with a token-injecting client wrapper.

**Success looks like:** interactive protocols (SSH, HTTP, DB) are usable
through the tunnel across a real corporate proxy, agents recover from
drops automatically, and all management happens in the console UI.

### Acceptance criteria (functional)

- An agent behind a plain-HTTP(S)-only network can register, connect,
  and serve one configured TCP target.
- A user can connect to the server's entry listener and reach the
  agent's target, using only a bearer token (no per-connection OTP).
- Multiple concurrent user connections to the same agent work without
  blocking each other (yamux multiplexing).
- When the SSE stream drops, the agent reconnects automatically and the
  server tears down stale streams (users retry; no hung connections).
- Admin can enroll agents/users (TOTP → bearer token), revoke tokens,
  and see agent/session status in the embedded console.
- Every server-side HTTP request is short-lived (< 30s) except the SSE
  GET; no streaming request bodies anywhere.

## Tech Stack

- **Backend:** Go 1.22+, module `github.com/wseternal/ssetunnel`
- **Deps (backend, exhaustive):** `github.com/hashicorp/yamux`,
  `github.com/pquerna/otp` — nothing else without asking
- **Console:** React 18 + TypeScript + Vite, built to
  `frontend/console/dist`, embedded via `go:embed`, served as SPA
  catch-all from the same HTTP server as the management API
  (auth-go `consoleserver` pattern; reimplement the ~40-line embedded-SPA
  handler if `orcacommon/litespaserver` is not importable)
- **Storage:** single JSON file (tokens, agents, users), `0600` perms,
  atomic write (temp file + rename)
- **TLS:** env-provided cert/key paths; `--allow-insecure` flag runs
  plain HTTP/TCP explicitly. No self-signed generation, no ACME.

## Commands

```bash
# Backend
go build ./...                          # build all packages
go test ./... -race -count=1            # unit + integration tests
go vet ./...                            # lint baseline

# Console
cd frontend/console && npm ci           # install
npm run dev                             # Vite dev server (proxy API to :8080)
npm run build                           # produce dist/ (embed target)

# Full build (console embed requires dist to exist)
cd frontend/console && npm run build && cd ../.. && go build ./cmd/ssetunnel

# Run
./ssetunnel server --config server.yaml
./ssetunnel agent --server https://tunnel.example.com --token $AGENT_TOKEN --target 127.0.0.1:3000
./ssetunnel connect --server tunnel.example.com:9090 --token $USER_TOKEN --local 13306
```

## Project Structure

```
cmd/ssetunnel/       → single binary, subcommands server|agent|connect
internal/transport/  → net.Conn adapter: SSE down + batched POST up,
                       batching writer, seq numbers, reorder window
internal/mux/        → yamux session setup, keepalive, stream handling
internal/auth/       → TOTP enrollment, bearer tokens, JSON token store
internal/server/     → HTTP server: /events, /up, /register, mgmt API,
                       TLS entry listener, session registry
internal/agent/      → agent client: connect, auto-reconnect, target dial
internal/connect/    → client wrapper: local listener, token injection
internal/consoleapi/ → management JSON API handlers
frontend/console/    → React+Vite console (src/, dist/ embedded)
docs/ideas/          → ideation one-pagers
docs/specs/          → this spec
tasks/               → plan.md, todo.md (next phases)
```

## Code Style

```go
// Batcher accumulates yamux writes into POST-sized frames.
// Flush fires at 16 KiB or after 25 ms, whichever comes first.
type Batcher struct {
	mu      sync.Mutex
	buf     []byte
	maxSize int
	maxWait time.Duration
	flush   func([]byte) error
}
```

- Package comments on every package; exported types get one-line doc
  comments stating the *why* where non-obvious
- No global mutable state; sessions and stores are constructed and
  injected (the reference sketch's package-level `session` var is the
  anti-pattern this codebase avoids)
- Errors wrapped with context: `fmt.Errorf("open stream: %w", err)`
- Table-driven tests; `t.Helper()` in test helpers
- Console: function components, no default exports, `camelCase` files

## Testing Strategy

- **Framework:** stdlib `testing` + `httptest`; no assertion libraries
- **Location:** `*_test.go` beside the code; integration tests in
  `internal/transport/` and `internal/server/`
- **Coverage:** transport (batching, sequencing, reorder, heartbeat
  filtering) and auth (TOTP window, token store atomicity) must have
  unit tests; proxying paths get end-to-end tests over `httptest`
- **Race detector:** `go test -race` is the required gate
- **Proxy simulation:** an integration test inserts an artificial
  middlebox (idle-timeout killer, body-size cap) between agent and
  server to prove the transport survives it
- **Performance harness:** `internal/transport/bench_test.go` measures
  throughput, added latency, and reconnect time against the budgets
  below — run manually before releases, not in CI

## Boundaries

- **Always:** run `go test ./... -race` before considering a task done;
  keep every non-SSE HTTP request short-lived; wrap errors with context
- **Ask first:** adding any Go dependency beyond yamux/otp; changing the
  wire protocol (frame format, headers, seq semantics); console
  dependency additions; anything in the idea doc's "Not Doing" list
- **Never:** streaming/chunked request bodies; per-connection OTP;
  secrets in logs or committed files; package-level mutable session
  state; removing failing tests without approval

## Success Criteria

Functional criteria as listed in Objective, plus these testable
performance budgets (interactive-first profile):

- **Added latency:** ≤ 50 ms per connection vs. direct connection,
  measured through the loopback proxy-simulation harness
- **Bulk throughput:** ≥ 5 MB/s single stream through the same harness
- **Concurrency:** 32 concurrent streams, no head-of-line blocking
  (one stalled stream doesn't halt others)
- **Reconnect:** agent re-establishes session < 5 s after SSE drop;
  no goroutine or memory leak across 100 reconnect cycles
- **SSE survival:** stream with 15 s heartbeats survives a 60 s
  idle-killing middlebox indefinitely

Done = all tests green under `-race`, budgets met in the bench harness,
console served from the embedded binary, and a live demo: SSH through
the tunnel with `ProxyCommand` using `ssetunnel connect`.

## Open Questions

- None blocking.
- (Resolved: perf profile = interactive-first; console auth = TOTP
  first login → session cookie valid N hours; module path =
  github.com/wseternal/ssetunnel.)
