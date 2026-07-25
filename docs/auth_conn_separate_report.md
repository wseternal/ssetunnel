# Auth / Connectivity Separation Review Report

> Generated: 2026-07-25
> Review scope: Full codebase — separation of raw connectivity from authentication, and readiness for user-centric auth model

---

## Context

The current authentication model requires **different tokens for agent and client sides**. In most real-world usage, the same user deploys the agent in a restricted network and then connects from a public client. The authentication should be **explicitly bound to the user**, not force the user to manage separate tokens for concrete agents and clients.

This review assesses:
1. Whether raw connectivity code is cleanly separated from authentication code
2. What architectural changes are needed to support a user-centric auth model

---

## Critical Issues (MUST FIX)

### 1. Transport layer embeds auth — `Conn` is not auth-agnostic

**Files:** [`internal/transport/conn.go#L30-L59`](../internal/transport/conn.go)

`transport.Config` carries `Token string` (L53) and `OnTokenUpgrade func` (L58). `Conn` stores the token, injects `Authorization: Bearer` headers on every `/events` GET (L156) and `/up` POST (L338), parses `X-SSET-Token` for PIN redemption (L179), and maps 401 → `ErrUnauthorized`. The byte-pipe layer owns the entire auth lifecycle.

**Fix:** Replace `Token` + `OnTokenUpgrade` with a `RequestModifier func(*http.Request)` callback. The agent provides auth injection; the transport stays protocol-agnostic.

```go
// In Config, replace Token/OnTokenUpgrade with:
RequestModifier func(*http.Request) // called before every HTTP request

// Agent would then provide:
cfg.RequestModifier = func(req *http.Request) {
    if a.Token != "" {
        req.Header.Set("Authorization", "Bearer "+a.Token)
    }
}
```

---

### 2. Server TCP entry path has inline auth with hardcoded role checks

**Files:** [`internal/server/server.go#L130-L154`](../internal/server/server.go)

`proxyEntry` inlines the entire token handshake + role validation (`tokInfo.Role != "user" && tokInfo.Role != "admin"`). Unlike the HTTP agent path (which uses `AgentAuthMiddleware`), this is raw auth code mixed with proxy logic. The role check is structurally incompatible with user-centric auth. Additionally, the server silently closes on auth failure without sending a rejection line, so the client sees `EOF` instead of `"ERR unauthorized"`.

**Fix:** Extract `authenticateEntryConn()` that returns `(TokenInfo, error)`. Send `"ERR unauthorized\n"` before closing on failure.

```go
func (s *Server) authenticateEntryConn(c net.Conn) (auth.TokenInfo, bool) {
    c.SetReadDeadline(time.Now().Add(5 * time.Second))
    reader := bufio.NewReader(c)
    tokenLine, err := reader.ReadString('\n')
    if err != nil {
        fmt.Fprintf(c, "ERR read error\n")
        return auth.TokenInfo{}, false
    }
    tokInfo, err := s.store.ValidateToken(context.Background(), strings.TrimSpace(tokenLine))
    if err != nil {
        fmt.Fprintf(c, "ERR unauthorized\n")
        return auth.TokenInfo{}, false
    }
    c.SetReadDeadline(time.Time{})
    return tokInfo, true
}
```

---

### 3. No user identity exists anywhere — schema-level blocker

**Files:** [`migrations/20260724000001_initial_schema.sql#L26-L37`](../migrations/20260724000001_initial_schema.sql), [`internal/auth/store.go#L20-L27`](../internal/auth/store.go)

No `users` table. No `user_id` column on `tokens`, `pins`, or `admin_sessions`. `TokenInfo` struct has `ID`, `Role`, `Description` — no `UserID`. Tokens are flat role assignments. There's no way to say "these tokens (agent + user) belong to the same person."

**Fix:** Add a `users` table, add nullable `user_id` FK to `tokens`/`pins`, add `UserID` to `TokenInfo`. This is the foundational step.

---

### 4. Token validation cache race condition allows revoked tokens to persist

**Files:** [`internal/auth/store.go#L104-L152`](../internal/auth/store.go)

`ValidateToken` has a read-through cache. Race:
1. Thread A (validate) starts DB query on cache miss
2. Thread B (revoke) commits DB update + deletes cache
3. Thread A's DB query returns pre-revocation state
4. Thread A caches the stale valid entry

The revoked token is then accepted **indefinitely** from cache.

**Fix:** Either remove the cache (tunnel load is low enough) or add a TTL (e.g. 30s):

```go
// Simplest correct fix: remove cache, always query DB
func (s *Store) ValidateToken(ctx context.Context, rawToken string) (TokenInfo, error) {
    digest := ComputeDigest(rawToken)
    // ... DB query only, no cache.Load/Store/Delete
}
```

---

### 5. RedeemPIN is not atomic — PIN consumed without producing a token

**Files:** [`internal/auth/store.go#L78-L91`](../internal/auth/store.go)

`RedeemPIN` calls `VerifyAndUsePIN` (marks PIN as `used_at` in DB), then `GenerateToken`, then `CreateToken`. If either of the latter two fails, the PIN is permanently consumed but no token exists. No recovery path.

**Fix:** Wrap in a single DB transaction so PIN consumption and token creation are atomic:

```go
func (s *Store) RedeemPIN(ctx context.Context, rawPIN string) (rawToken, role string, err error) {
    tx, err := s.pool.Begin(ctx)
    if err != nil {
        return "", "", fmt.Errorf("begin tx: %w", err)
    }
    defer tx.Rollback(ctx)

    digest := ComputeDigest(rawPIN)
    var txRole string
    err = tx.QueryRow(ctx, `
        UPDATE pins SET used_at = CURRENT_TIMESTAMP
        WHERE digest = $1 AND used_at IS NULL AND expires_at > CURRENT_TIMESTAMP
        RETURNING role
    `, digest).Scan(&txRole)
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return "", "", ErrInvalidPIN
        }
        return "", "", fmt.Errorf("verify pin: %w", err)
    }

    rawToken, err = GenerateToken()
    if err != nil {
        return "", "", fmt.Errorf("generate token: %w", err)
    }

    tokenDigest := ComputeDigest(rawToken)
    _, err = tx.Exec(ctx,
        `INSERT INTO tokens (digest, role, description) VALUES ($1, $2, $3)`,
        tokenDigest, txRole, "auto-generated from PIN redemption")
    if err != nil {
        return "", "", fmt.Errorf("store token: %w", err)
    }

    if err := tx.Commit(ctx); err != nil {
        return "", "", fmt.Errorf("commit: %w", err)
    }
    return rawToken, txRole, nil
}
```

---

### 6. Console login bypass when TOTP secret is empty

**Files:** [`internal/consoleapi/router.go#L46-L86`](../internal/consoleapi/router.go)

When `totpSecret` is empty, the TOTP check is completely bypassed — anyone can `POST /api/v1/login` and receive a valid admin session cookie. `totpSecret` defaults to env var `SSETUNNEL_TOTP_SECRET` which may be unset in production.

**Fix:**

```go
if a.totpSecret == "" {
    http.Error(w, "TOTP not configured on server", http.StatusServiceUnavailable)
    return
}
if !auth.VerifyTOTP(a.totpSecret, req.TOTPCode) {
    http.Error(w, "invalid TOTP code", http.StatusUnauthorized)
    return
}
```

---

## Warnings (SHOULD FIX)

### 7. Dual-protocol auth model: HTTP Bearer vs TCP line protocol

**Files:** [`internal/connect/client.go#L157-L186`](../internal/connect/client.go), [`internal/server/server.go#L130-L154`](../internal/server/server.go)

Agent authenticates via HTTP `Authorization: Bearer` (checked by `AgentAuthMiddleware` with PIN redemption). Connect client authenticates via custom TCP line protocol (`token\n` → `OK\n`, no PIN redemption). Two incompatible auth mechanisms for the same conceptual operation. Any auth refactor must touch both paths.

---

### 8. Role-based authorization hardcoded as string literals in 4 locations

**Files:** [`internal/server/middleware.go#L44-L48`](../internal/server/middleware.go), [`internal/server/server.go#L143`](../internal/server/server.go)

```
middleware.go:45  →  tokInfo.Role == "agent" || tokInfo.Role == "admin"
middleware.go:52  →  role == "agent" || role == "admin"
middleware.go:77  →  tokInfo.Role == "admin"
server.go:143     →  tokInfo.Role != "user" && tokInfo.Role != "admin"
```

A permission/capability abstraction must replace these for user-centric auth.

---

### 9. Agent `Token` field mutated without synchronization

**Files:** [`internal/agent/agent.go#L111-L113`](../internal/agent/agent.go)

`OnTokenUpgrade` writes `a.Token = newToken` from the transport goroutine during `DialAgent`. `a.Token` is read by subsequent `runOnce` calls. No mutex or atomic. Currently works because PIN redemption happens once, but would break under frequent token refresh.

---

### 10. TCP entry handshake has no protocol framing or versioning

**Files:** [`internal/connect/client.go#L164-L165`](../internal/connect/client.go)

Client sends bare `<token>\n` as first TCP bytes. No version byte, no message type, no way to extend with additional auth context (user ID, nonce, intent). Any protocol extension requires a breaking change or version prefix.

---

### 11. Connect client has no PIN redemption path

**Files:** [`internal/connect/client.go#L157-L186`](../internal/connect/client.go)

The agent can authenticate with a single-use PIN that gets redeemed for a persistent token (via `AgentAuthMiddleware` fallback + `X-SSET-Token` response header + `OnTokenUpgrade` callback). The connect client has no such capability — it can only present a pre-existing token. This asymmetry means the enrollment flow only works for agents, not for connect clients.

---

## Suggestions (CONSIDER)

### 12. `/probe` endpoint is intentionally auth-free

**Files:** [`internal/server/handlers.go#L67`](../internal/server/handlers.go), [`internal/probe/probe.go`](../internal/probe/probe.go)

Intentional (cycle-2 plan decision 6). In a user-centric model, even diagnostic endpoints may want rate limiting. Low priority.

### 13. `Server.SetAuthStore` rebuilds the entire handler

**Files:** [`internal/server/server.go#L55-L59`](../internal/server/server.go)

Functional but fragile. Handler replacement means `OnSession` must be re-wired. Minor architectural smell.

---

## Auth/Connect Separation Assessment

| Dimension | Current State | Refactoring Effort |
|-----------|--------------|-------------------|
| **auth package isolation** | ✅ Clean — no connectivity imports | Low — add `users` table, user-scoped tokens |
| **mux package** | ✅ Clean — zero auth awareness | None needed |
| **probe package** | ✅ Clean — no auth | Low — may want auth later |
| **transport package** | ❌ `Conn` embeds token + auth headers | **High** — must extract auth into callback |
| **agent package** | ⚠️ Holds token, passes to transport | Medium — provide auth via RequestModifier |
| **connect package** | ⚠️ Inline TCP handshake protocol | **High** — protocol change or HTTP migration |
| **server (HTTP path)** | ⚠️ Middleware is role-specific | Medium — generalize role checks |
| **server (TCP entry)** | ❌ Inline auth + role-specific checks | **High** — extract middleware, unify with agent path |
| **database schema** | ❌ No user identity | Medium — add `users` table, migrate tokens |

**Overall verdict:** The codebase achieves **partial** separation. The `auth/`, `mux/`, and `probe/` packages are cleanly isolated. However, the **transport layer** and **server TCP entry path** have auth logic structurally embedded, and the **dual-protocol auth model** (HTTP Bearer for agents, TCP line protocol for clients) is the fundamental architectural barrier to user-centric auth.

---

## Current Auth Touchpoints

### Database Schema

| Table | Columns | Issue |
|-------|---------|-------|
| `tokens` | `digest`, `role`, `description`, `expires_at`, `revoked_at` | No `user_id` |
| `pins` | `digest`, `role`, `expires_at`, `used_at` | No `user_id` |
| `admin_sessions` | `digest`, `expires_at` | Separate realm, no link to tokens/users |

### Wire Protocols

| Path | Protocol | Auth Mechanism |
|------|----------|----------------|
| Agent → Server (HTTP) | `Authorization: Bearer <token>` on `/events` and `/up` | `AgentAuthMiddleware` (role: agent/admin), PIN redemption |
| Client → Server (TCP) | `<token>\n` → `OK\n` | Inline in `proxyEntry` (role: user/admin), no PIN redemption |
| Console → Server (HTTP) | Cookie-based admin session | `AdminSessionMiddleware` + TOTP login |

### CLI / Env Vars

| Command | Flag | Env Var | Notes |
|---------|------|---------|-------|
| `agent` | `--token` | `SSETUNNEL_TOKEN` | Agent credential |
| `connect` | `--token` | `SSETUNNEL_TOKEN` | User credential (same env var!) |

### Go Structs

| Struct | Auth Fields | Issue |
|--------|------------|-------|
| `agent.Agent` | `Token string` | Mutated without sync on upgrade |
| `connect.Client` | `token string` | No extensibility |
| `transport.Config` | `Token string`, `OnTokenUpgrade func` | Auth embedded in transport config |
| `auth.TokenInfo` | `ID`, `Role`, `Description`, timestamps | No `UserID` |

---

## Recommended Refactoring Order

### Phase 1: Security Fixes (immediate, non-breaking)

1. **Fix token cache race** (#4) — remove cache or add TTL
2. **Make RedeemPIN atomic** (#5) — wrap in DB transaction
3. **Fix TOTP bypass** (#6) — reject login when TOTP unconfigured
4. **Send rejection on TCP auth failure** (#2) — `"ERR unauthorized\n"` before close

### Phase 2: Schema Preparation (non-breaking)

5. Add `users` table (`id`, `email`/`name`, `created_at`)
6. Add nullable `user_id` FK to `tokens`, `pins`, `admin_sessions`
7. Add `UserID *int64` to `TokenInfo` struct
8. Update `CreateToken` / `CreatePIN` to accept optional `userID`

### Phase 3: Transport Decoupling (non-breaking)

9. Extract auth from `transport.Conn` into `RequestModifier` callback (#1)
10. Agent provides auth injection; transport becomes auth-agnostic

### Phase 4: Permission Layer (non-breaking)

11. Introduce `Permissions` type (bit flags or `[]string`)
12. Add `PermissionsFor(role string) Permissions` mapping
13. Replace hardcoded role checks in middleware with permission checks (#8)
14. Keep role as backward-compatible field on tokens

### Phase 5: Protocol Unification (breaking)

15. Version the TCP entry handshake (`v2:<token>\n` or move to HTTP)
16. Add PIN redemption to connect client path (#11)
17. Unify role checks: accept any valid token for both agent and client

### Phase 6: User Identity Integration (breaking)

18. Make `user_id` non-nullable on `tokens` and `pins`
19. Update CLI flags to support user credentials
20. Update Console API and frontend to be user-aware
21. Redesign PIN enrollment to be user-bound
22. Merge or relate `admin_sessions` with user identity

---

## Migration Risk Assessment

| Risk | Severity | Probability | Mitigation |
|------|----------|-------------|------------|
| Schema migration breaks running servers | Critical | High | Nullable `user_id` initially; migration script for existing tokens |
| Role→Permission mapping is lossy | High | Medium | Permission set must be superset of current roles |
| CLI flag/env var conflict | Medium | High | Consider `SSETUNNEL_AGENT_TOKEN` / `SSETUNNEL_USER_TOKEN` |
| TCP handshake protocol lock-in | Medium | Low | Version prefix (`v2:`) or migrate to HTTP |
| Console API contract break | Medium | Medium | Version the API or add user fields alongside role |
| Test suite assumes role model | Low | High | All tests use explicit role strings — must be updated |

---

## Summary

- **`internal/auth/`** is cleanly isolated with zero connectivity imports — good foundation. However, the **schema has no user identity** (no `users` table, no `user_id` FK), the **token cache has a revocation race**, and **PIN redemption is non-atomic**.
- **Transport layer (`internal/transport/conn.go`)** violates separation by embedding token storage, Bearer header injection, and PIN upgrade callbacks — the **single largest architectural barrier** to refactoring auth.
- **Server** has two divergent auth paths: HTTP middleware for agents (`"agent"||"admin"` + PIN redemption) vs inline TCP handshake for clients (`"user"||"admin"`, no PIN redemption, silent close on failure). **Role checks are hardcoded string literals** in 4 locations.
- **Wire protocols** are structurally incompatible: HTTP Bearer (agent) vs bare TCP line (client). The TCP handshake has **no versioning** for future extension.
- **Security**: Console login is **completely open when TOTP is unconfigured** — anyone gets admin access.
