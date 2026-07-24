# Implementation Plan: PIN Redemption Bug Fix

## Problem

Agent connection with a single-use PIN fails with 401:
```
ssetunnel agent --server http://localhost:8080 --target 127.0.0.1:22 --token EMNBGDOK
```

**Root cause**: `AgentAuthMiddleware` only calls `store.ValidateToken()`, which queries the `tokens` table. PINs live in the `pins` table. `VerifyAndUsePIN()` exists but is never called anywhere.

## Approach: Inline PIN Redemption in Middleware

When token validation fails, fall back to PIN redemption. On success, return a persistent token via `X-SSET-Token` response header. The agent picks it up for reconnections.

### Why inline over a dedicated exchange endpoint?
- No new endpoint, no agent pre-flight logic, no extra round-trip
- `X-SSET-Token` response header pattern matches existing `X-SSET-Caps` convention
- Agent doesn't need to know whether its credential is a PIN or a token
- PINs (8-char base32) and tokens (64-char hex) never collide in format

## Flow

```
Agent starts with PIN "EMNBGDOK"
  → GET /events?id=xxx  Authorization: Bearer EMNBGDOK
  → AgentAuthMiddleware:
      1. ValidateToken("EMNBGDOK") → FAIL (not in tokens table)
      2. RedeemPIN("EMNBGDOK"):
         a. VerifyAndUsePIN → marks PIN used, returns role="agent"
         b. GenerateToken → "a1b2c3...64hex"
         c. CreateToken → stores in tokens table
      3. Set header: X-SSET-Token: a1b2c3...64hex
      4. Proceed to handleEvents
  → Agent reads X-SSET-Token, updates a.Token in memory
  → On reconnect: uses persistent token directly (ValidateToken succeeds)
```

## Changes

### 1. `internal/auth/store.go` — Add `RedeemPIN` method (~15 lines)
```go
func (s *Store) RedeemPIN(ctx context.Context, rawPIN string) (rawToken, role string, err error)
```
- Calls `VerifyAndUsePIN` → `GenerateToken` → `CreateToken`
- Atomic from the caller's perspective

### 2. `internal/server/middleware.go` — PIN fallback in `AgentAuthMiddleware` (~10 lines)
- When `ValidateToken` fails, try `RedeemPIN` as fallback
- On success: set `X-SSET-Token` response header, proceed to handler
- Race-safe: `VerifyAndUsePIN` uses `UPDATE ... WHERE used_at IS NULL RETURNING role` (atomic at PG row level)

### 3. `internal/transport/conn.go` — Read upgrade token from response (~7 lines)
- Add `OnTokenUpgrade func(newToken string)` to `Config`
- In `DialAgent`: after 200 response, read `X-SSET-Token` header, invoke callback

### 4. `internal/agent/agent.go` — Wire callback for reconnection (~4 lines)
- Pass `OnTokenUpgrade` in `runOnce` that updates `a.Token`
- Subsequent reconnections use the persistent token directly

### 5. Tests
- `internal/auth/store_test.go` — `TestRedeemPIN`
- `internal/server/middleware_test.go` — PIN auth + X-SSET-Token header + single-use enforcement
- `internal/server/e2e_test.go` — Full agent dial with PIN → token upgrade → reconnect

### 6. Frontend: Role selector + clearer tab naming (`frontend/console/src/App.tsx`)

**Problem**: The "Generate Token" dialog hardcodes `role: 'agent'` with no way to create `user`-role tokens. The "Tokens" tab name is ambiguous — it doesn't distinguish persistent bearer tokens from single-use enrollment PINs.

**Changes**:
- **Rename tabs** for clarity:
  - `Tokens` → `Bearer Tokens`
  - `Agent Enrollment` → `Enrollment PINs`
- **Add role dropdown** to the "Generate Bearer Token" dialog:
  - Options: `agent` / `user` / `admin`
  - Default: `agent`
- **Update dialog title** to reflect role: e.g. "Generate Agent Token" / "Generate User Token"
- **Rename "Generate Token" button** → "Generate Bearer Token" (already close, just ensure consistency)
- **Update PIN tab heading** to clarify these are single-use temporary credentials vs the persistent tokens on the other tab

### 7. Server: Per-session yamux instead of singleton (`internal/server/server.go`, `session.go`)

**Problem**: `Server.sess` is a single global `*yamux.Session`. Every new `/events` connection calls `attach()` which replaces it and closes the old yamux — killing all active streams for the previous agent. This causes:
- Agent disconnects when curl or another client hits `/events` (even with a different session ID)
- The 60-90s periodic disconnects when anything creates a new session

**Root cause**: `attach()` does `old := s.sess; s.sess = ms; old.Close()` — one yamux per server, not per agent.

**Fix**: Move the yamux session into the `Session` struct so each agent has its own independent yamux.

**Changes**:
- **`internal/server/session.go`**: Add `yamuxSess *yamux.Session` field to `Session`. Add `YamuxSession()` getter. Update `Close()` to also close the yamux session.
- **`internal/server/server.go`**:
  - Remove `s.sess *yamux.Session` singleton field from `Server`
  - Change `attach()` to store the yamux on the `Session` itself, not on `Server`. No more `old.Close()`.
  - Change `proxyEntry()` to pick the first available session's yamux from the registry (or add a session ID header for explicit routing later).
  - Remove `AttachConn()` / `AttachSession()` or update them to work with per-session yamux.
- **Cleanup**: When a session's `handleEvents` returns, the deferred `Remove` + `Session.Close()` now properly tears down that session's yamux without affecting others.

**Impact on existing tests**: Tests that use `Server.AttachConn()` or rely on `s.sess` will need updating.

## Risks

| Risk | Mitigation |
|------|------------|
| Agent crashes after PIN redemption before saving token | Token logged to stderr; operator uses it on restart. Re-enrollment needed if lost. |
| Two concurrent requests with same PIN | PG `UPDATE ... RETURNING` is atomic — only one wins, other gets 401 |
| DB write on hot path | Only once per agent lifetime; subsequent connections use cached token |
| Per-session yamux changes break existing e2e tests | Update tests in same PR; the `AttachConn` helper becomes `Session.AttachYamux` |

## Total: ~250 lines across 10 files, no new dependencies, no schema changes

---

# Previous Plan - ssetunnel Cycle 3 (Auth, Management Console, Connect Wrapper)

## Architecture & Design

1. **Database & Schema Management (`schema.hcl`, `atlas.hcl`, `migrations/`)**:
   - Atlas declarative schema in `schema.hcl` (`tokens`, `pins`, `admin_sessions`).
   - Versioned SQL migrations in `migrations/` embedded via `//go:embed` (`migrations.FS`).
   - Startup auto-migrations using `orcapostgres.OpenPool` with `orcapostgres.NewMigrator(migrations.FS, nil)`.
   - Integration tests use `testcontainers` (`orcapostgres.DBConfig{ DatabaseURLTemplate: "postgres:tc:" }`).
   - Optional auth bypass if `--disable-auth` or no DB config is passed (100% backward compatibility for existing cycle 1/2 tests).

2. **Authentication Core & High Performance (`internal/auth/`)**:
   - Stores cryptographically secure SHA-256 digests (`crypto/rand` 32-byte tokens / base32 PINs).
   - In-memory `sync.Map` read-through token cache for sub-microsecond validation on high-frequency `/up` POST batch paths.
   - Atomic single-use PIN consumption via `UPDATE pins SET used_at = NOW() WHERE ... RETURNING role`.
   - TOTP validation via `pquerna/otp/totp`.

3. **High-Throughput Stream Proxying & Handshake (`internal/server/`, `internal/connect/`)**:
   - Line-delimited TCP handshake (`<token>\n` → `OK\n`) with 5s read deadline on user TCP entry connection.
   - Reusable `sync.Pool` (32KB buffers) with `io.CopyBuffer` to eliminate GC allocation churn during bidirectional stream proxying.
   - `ssetunnel connect` wrapper supports both **Local Port Mode** (`--local 13306`) and **Stdio Mode** (`--local -`) for SSH `ProxyCommand`.

4. **Embedded React Console & JSON Management API (`frontend/`, `internal/consoleapi/`)**:
   - React 18 + Vite + Material UI (`@mui/material`) in `frontend/console/`, built to `frontend/console/dist`, embedded in Go via `frontend/frontend.go`.
   - SPA catch-all routing powered by `github.com/visdomtech/orcacommon/litespaserver`.
   - Admin TOTP login yielding 12h HTTP-only session cookie.
   - JSON management API (`/api/v1/login`, `/api/v1/tokens`, `/api/v1/enroll`, `/api/v1/sessions`).
   - Live metrics (active sessions, throughput bytes) backed by Go in-memory `sync/atomic` counters on `Session` and `Registry` (zero DB write overhead).

## Rejected Alternatives

1. **File-based JSON Token Storage**: Rejected in favor of PostgreSQL + Atlas + `orcacommon/postgres` per user request and requirement to match `auth-go` standard.
2. **Direct DB queries on every `/up` POST batch**: Rejected because high-frequency `/up` batches (hundreds/sec) would saturate DB connection pools. Replaced with `sync.Map` in-memory read-through cache.
3. **Database-backed Live Traffic Metrics**: Rejected because updating DB rows on every proxied byte/frame creates severe write contention. Replaced with Go in-memory `sync/atomic` counters.
