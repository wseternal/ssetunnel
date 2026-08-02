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
CreateUserSession(userID, rawToken, ttl) → stored with expiry
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
SaveSession(serverURL, token, username, role) → writes JSON
LoadSession(serverURL) → (token, resolvedServer, err)  # empty URL → first entry (sorted)
SessionServers() → []string  # list stored server URLs
```

Legacy plain-text format is detected and discarded with a warning; users must re-run `ssetunnel login`.

## Token Generation

`GenerateToken()` produces URL-safe random tokens. `GeneratePIN()` produces short numeric PINs. `ComputeDigest()` hashes tokens with SHA-256 for storage. `GenerateRecoveryCodes(count)` produces human-readable recovery codes. `GenerateTOTPSecret(issuer, username)` creates TOTP key + URL.

## Rules
* Tokens are stored as SHA-256 digests — raw tokens are never persisted.
* Use native pgx `[]string` scanning for `text[]` columns (not `pq.Array()`).
* Use `time.Time` for timestamp columns (pgx returns binary timestamps).
* Recovery codes use HMAC-SHA256 when pepper is set, SHA-256 otherwise.
* `EnsureAdminUser` uses `INSERT ... ON CONFLICT DO NOTHING` for concurrent startup races.
* Password validation returns `ErrUserNotFound` (not a distinct error) to prevent user enumeration.
