# Auth Migration Spec: User-Centric Authentication

> Approved: 2026-07-25
> Status: Plan — implementation in subsequent dev cycles

## Intent

- **Outcome:** `ssetunnel login` (user+password+OTP) is the single auth entry point. All commands (`agent`, `connect`) inherit the stored session. No tokens to manage manually.
- **User:** A person who deploys an agent in a restricted network (SSHing in) and connects to it from elsewhere. Same person, one identity.
- **Success:** User runs `ssetunnel login` once per machine; agent deploys as "user X's agent," connect works as "user X," no `--token` flags.
- **Constraint:** RBAC managed in console controls permissions (connect to agents, bring own agent). Server enforces.
- **Out of scope:** External SSO/OAuth, multi-tenancy, agent-to-agent auth, wire transport changes (SSE+POST stays).

---

## Phase 0: Security Fixes (non-breaking)

| Step | File | Change |
|------|------|--------|
| 0.1 | `internal/auth/store.go` | Remove `sync.Map` cache from `ValidateToken` — always query DB |
| 0.2 | `internal/auth/store.go` | Wrap `RedeemPIN` in a single DB transaction |
| 0.3 | `internal/consoleapi/router.go` | Return 503 when TOTP not configured instead of skipping |
| 0.4 | `internal/server/server.go` | Extract `authenticateEntryConn()`, send `"ERR unauthorized\n"` on failure |

**Dev cycle:** 1

---

## Phase 1: Schema Foundation (non-breaking, additive)

| Step | File | Change |
|------|------|--------|
| 1.1 | `migrations/` (new) | Add `users` table: `id`, `username` (unique), `password_hash`, `totp_secret`, `role`, `created_at`, `disabled_at` |
| 1.2 | `migrations/` (new) | Add nullable `user_id` FK to `tokens`, `pins`, `admin_sessions` |
| 1.3 | `migrations/` (new) | Add `user_sessions` table: `id`, `user_id` (NOT NULL), `digest` (unique), `created_at`, `expires_at` |
| 1.4 | `schema.hcl` | Update Atlas schema to match |
| 1.5 | `internal/auth/store.go` | Add `UserID *int64` to `TokenInfo`; add `CreateUser`, `ValidatePassword`, `CreateUserSession`, `ValidateUserSession` methods |

**Dev cycle:** 1

---

## Phase 2: Login, Password Auth & Console CRUD

| Step | File | Change |
|------|------|--------|
| 2.1 | `internal/auth/password.go` (new) | `HashPassword` / `CheckPassword` using `golang.org/x/crypto/bcrypt` |
| 2.2 | `internal/auth/session_file.go` (new) | `SaveSession` / `LoadSession` for `~/.ssetunnel/session` (file mode 0600) |
| 2.3 | `internal/consoleapi/router.go` | Add endpoints: `POST /api/v1/user-login`, `POST /api/v1/users`, `GET /api/v1/users`, `PATCH /api/v1/users/{id}`, `DELETE /api/v1/users/{id}`, `GET /api/v1/me` |
| 2.4 | `cmd/ssetunnel/main.go` | Add `ssetunnel login` command (interactive user+pass+OTP, stores session file) |
| 2.5 | `frontend/console/src/App.tsx` | **Add:** Users tab (list/create/edit/disable users), updated login form (username+password+OTP). **Modify:** Sessions tab to show user-bound sessions. **Keep:** Tokens and PINs tabs (removed in Phase 5). |

**Dev cycles:** 1 (backend) + 1 (frontend)

---

## Phase 3: Session-Based Auth for Agent & Connect

| Step | File | Change |
|------|------|--------|
| 3.1 | `internal/transport/conn.go` | Add `RequestModifier func(*http.Request)` to `Config`; when set, takes precedence over `Token` |
| 3.2 | `internal/agent/agent.go` | Session-based auth via `RequestModifier` closure; `atomic.Pointer[string]` for concurrent-safe token |
| 3.3 | `cmd/ssetunnel/main.go` | `runAgent` loads session from `~/.ssetunnel/session`; deprecation warning on `--token` |
| 3.4 | `internal/connect/client.go` | `Client` reads session token from file |
| 3.5 | `internal/server/middleware.go` | Add `UserSessionMiddleware`: validates session tokens with `PermAgent` |
| 3.6 | `internal/server/server.go` | `authenticateEntryConn` accepts user-session tokens with `PermConnect` |
| 3.7 | `internal/server/handlers.go` | Agent registration records `user_id` on session |

**Dev cycles:** 1 (transport+agent) + 1 (server+connect)

---

## Phase 4: Permission Layer

| Step | File | Change |
|------|------|--------|
| 4.1 | `internal/auth/permissions.go` (new) | `type Permission string` constants: `PermAgent`, `PermConnect`, `PermAdmin`. `PermissionsFor(role string) []Permission` |
| 4.2 | `internal/server/middleware.go` | Replace hardcoded role strings with `hasPermission(tokInfo, PermX)` |
| 4.3 | `internal/server/server.go` | Replace entry role check with `hasPermission(tokInfo, PermConnect)` |

**Dev cycle:** 1

---

## Phase 5: Cleanup (breaking, deferred)

| Step | File | Change |
|------|------|--------|
| 5.1 | `cmd/ssetunnel/main.go` | Remove `--token` flags and `SSETUNNEL_TOKEN` env |
| 5.2 | `internal/auth/store.go`, `pin.go` | Remove PIN enrollment flow |
| 5.3 | `internal/consoleapi/router.go` | Remove legacy token CRUD endpoints, TOTP-only login fallback |
| 5.4 | `frontend/console/src/App.tsx` | **Remove:** Tokens tab, PINs tab. **Modify:** Sessions tab (user-only). **Update:** Navigation (Dashboard, Users, Sessions, Agents). |
| 5.5 | `migrations/` (new) | Make `user_id` non-nullable, drop `pins`, collapse `admin_sessions` into `user_sessions` |

**Dev cycle:** 1

---

## Dependency Graph

```
Phase 0 (security)     ← no deps
Phase 1 (schema)       ← depends on Phase 0
Phase 2 (login+CRUD)   ← depends on Phase 1
Phase 4 (permissions)  ← depends on Phase 1 (can parallel with Phase 2)
Phase 3 (agent/connect)← depends on Phase 2 + Phase 4
Phase 5 (cleanup)      ← depends on Phase 3 stable in production
```

**Critical path:** 0 → 1 → 2 → 3 → 5

**Total dev cycles:** ~8 (one per phase, Phase 2 and 3 split into 2 each)

---

## Rejected Alternatives

1. **Argon2id over bcrypt:** Login is rare (once per machine). Bcrypt is sufficient, simpler API, `x/crypto` already in deps.

2. **Two-tier session validation cache:** Old cache had correctness bug (race condition). Tunnel load is low — DB queries are sub-millisecond. Not worth the risk.

3. **Embedded `permissions TEXT[]` in sessions table:** Denormalization means RBAC changes require session invalidation. Deriving from role at validation time is simpler and immediately effective.

4. **Versioned TCP handshake `v2:`:** Deferred to `docs/next_phases.md`. Current wire format works fine with session tokens. No extensibility needed for this migration.
