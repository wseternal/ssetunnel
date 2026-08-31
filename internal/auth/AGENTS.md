# Auth

PostgreSQL-backed authentication: bearer tokens, user sessions, per-user TOTP with recovery codes, agent configs, user permissions, password hashing, and multi-server session files.

## Core Type: `Store`

Backed by `pgxpool.Pool`. All operations hit the database directly (no in-process cache). Optional HMAC pepper for recovery code digests (`SetRecoveryCodePepper`).

## Token Lifecycle

```
CreateToken(rawToken, role, description, expiresAt)
    ↓
ValidateToken(rawToken) → TokenInfo
    ↓
RevokeToken(rawToken) / RevokeTokenByID(id)
```

Roles: `"admin"`, `"user"`, `"agent"` (agent role is for bearer tokens only, not user accounts).

## User Sessions

```
CreateUserSession(userID, rawToken, ttl) → (expiresAt time.Time, err)  # returns stored expiry to avoid caller skew
    ↓
ValidateUserSession(rawToken) → UserSessionInfo (joins users table for role + permissions)
    ↓
RevokeUserSession(rawToken) → deletes from DB
```

Used by console login/logout to immediately invalidate the session server-side. Sessions have a 30-day TTL.

## User Management

```
CreateUser(username, passwordHash, role, permConnect, permAgent) → UserInfo
GetUserByUsername(username) / GetUserByID(id) → UserInfo
UpdateUserWithDisabled(id, role, passwordHash, permConnect, permAgent, disabled) → updates
DeleteUser(id) → cascades to user_sessions + tokens
EnsureAdminUser(ctx) → creates 'admin' with random password if no admin exists
```

Users can be disabled via `disabled_at` timestamp. Disabled users fail session validation.

## Per-User TOTP & Recovery Codes

```
UserTOTPEnrolled(username) → (enrolled, found, err)  # constant-time, anti-enumeration
SetTOTPSecret(userID, secret) → stores per-user TOTP secret
SetTOTPSecretAndRecoveryCodes(userID, secret, digests) → atomic tx
ClearTOTPAndRecoveryCodes(userID) → atomic tx
AnyTOTPEnrolled(ctx) → bool (server startup warning)
```

Recovery codes: 8 codes per user, stored as HMAC-SHA256 (with pepper) or SHA-256 digests. Consumed atomically via `ConsumeRecoveryCode`. TOTP verification uses `pquerna/otp`.

## Password Hashing

`HashPassword(plaintext)` uses bcrypt. `CheckPassword(hash, plaintext)` validates. Password validation errors return `ErrUserNotFound` to prevent user enumeration.

## Agent Configs

```
CreateAgentConfig(agentID, description, allowedTargets) → AgentConfig
    ↓
GetAgentConfig(agentID) → finds by agent_id or falls back to NULL (default)
    ↓
ListAgentConfigs() → all configs including NULL default row
    ↓
UpdateAgentConfig(id, agentID, description, allowedTargets) → updated config
    ↓
DeleteAgentConfig(id) → error if NULL row (ErrCannotDeleteDefault)
```

**Target validation**: `TargetAllowed(patterns, addr)` matches addresses against patterns:
- `*` — allow all
- `host:*` — any port on host
- `host:port` — exact match
- `host` (no port) — match host with any port

The NULL `agent_id` row serves as the default config for agents without a specific config.

## Permissions

```
PermissionsFor(role) → [Permission]        # admin: all, user: connect, agent: agent
HasPermission(role, perm) → bool           # role-based check
UserHasPermission(role, permConnect, permAgent, perm) → bool  # admin always true
```

Permission constants: `PermAgent`, `PermConnect`, `PermAdmin`.

## Session File

Multi-server session storage at `~/.ssetunnel/session` (JSON format):
```
SaveSession(serverURL, token, username, role, consoleURL string, expiresAt time.Time) → writes JSON
LoadSession(serverURL) → (token, resolvedServer, consoleURL string, expiresAt time.Time, err)
UpdateSessionToken(serverURL, newToken string, expiresAt time.Time) → updates token+expiry only
SessionServers() → ([]string, error)
```

Session entries include `expires_at` (RFC 3339) and `console_url` fields (both `omitempty` for backward compatibility). `UpdateSessionToken` preserves all other fields when rotating tokens.

Legacy plain-text format is detected and discarded with a warning; users must re-run `ssetunnel login`.

## Session Refresh

```
NeedsRefresh(expiresAt time.Time) → bool   # true when remaining TTL < 7 days; false for zero time
RefreshSession(consoleURL, token string) → (newToken string, newExpiresAt time.Time, err)
```

`RefreshSession` calls `POST /console/api/v1/refresh-session` with a 15-second HTTP client timeout. Validates non-empty token in response. Validates `consoleURL` scheme: requires HTTPS for non-localhost hosts (SSRF protection), allows HTTP only for localhost/127.0.0.1/::1. Client-side refresh is transparent — integrated in `resolveServerURL`.

## Store: Session Refresh

```
RefreshUserSession(ctx, rawToken string, ttl time.Duration) → (*RefreshResult, error)
```

Atomic token rotation: validate→create→delete in a single PostgreSQL transaction with `FOR UPDATE` row locking. Token generation (`GenerateToken`) runs before `Begin()` to minimize lock hold time.

## Token Generation

`GenerateToken()` produces URL-safe random tokens. `ComputeDigest()` hashes tokens with SHA-256 for storage. `ComputeHMACDigest()` hashes with HMAC-SHA256 using the configured pepper. `GenerateRecoveryCodes(n)` produces `n` human-readable recovery codes. `GenerateTOTPSecret(issuer, username)` creates TOTP key + URL.

## Rules
* Tokens are stored as SHA-256 digests — raw tokens are never persisted.
* Use native pgx `[]string` scanning for `text[]` columns (not `pq.Array()`).
* Use `time.Time` for timestamp columns (pgx returns binary timestamps).
* Recovery codes use HMAC-SHA256 when pepper is set, SHA-256 otherwise.
* `EnsureAdminUser` uses `INSERT ... ON CONFLICT DO NOTHING` for concurrent startup races.
* Password validation returns `ErrUserNotFound` (not a distinct error) to prevent user enumeration.
