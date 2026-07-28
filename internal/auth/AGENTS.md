# Auth

PostgreSQL-backed authentication: bearer tokens, user sessions, TOTP, agent configs, and user permissions.

## Core Type: `Store`

Backed by `pgxpool.Pool`. All operations hit the database directly (no in-process cache).

## Token Lifecycle

```
CreateToken(rawToken, role, description, expiresAt)
    ↓
ValidateToken(rawToken) → TokenInfo (cached after first DB read)
    ↓
RevokeToken(rawToken) / RevokeTokenByID(id) → evicts cache
```

Roles: `"agent"`, `"user"`, `"admin"`.

## User Sessions

```
CreateUserSession(userID, rawToken, ttl) → stored with expiry
    ↓
ValidateUserSession(rawToken) → UserInfo
    ↓
RevokeUserSession(rawToken) → deletes from DB
```

Used by console logout to immediately invalidate the session server-side.

## TOTP

`VerifyTOTP(secret, code)` validates time-based OTP codes for console login. Uses `pquerna/otp`.

## Token Generation

`GenerateToken()` produces URL-safe random tokens. `GeneratePIN()` produces short numeric PINs. `ComputeDigest()` hashes tokens with SHA-256 for storage.

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

The NULL `agent_id` row serves as the default config for agents without a specific config.

## User Permissions

Users have boolean flags: `can_connect` and `can_create_agent`. Checked during token validation and agent handshake.

## Rules
* Tokens are stored as SHA-256 digests — raw tokens are never persisted.
* Use native pgx `[]string` scanning for `text[]` columns (not `pq.Array()`).
* Use `time.Time` for timestamp columns (pgx returns binary timestamps).
