# Auth

PostgreSQL-backed authentication: bearer tokens, single-use PINs, admin sessions, and TOTP.

## Core Type: `Store`

Backed by `pgxpool.Pool` with a `sync.Map` read-through cache for token validation.

## Token Lifecycle

```
CreateToken(rawToken, role, description, expiresAt)
    ↓
ValidateToken(rawToken) → TokenInfo (cached after first DB read)
    ↓
RevokeToken(rawToken) / RevokeTokenByID(id) → evicts cache
```

Roles: `"agent"`, `"user"`, `"admin"`.

## PIN Lifecycle

```
CreatePIN(rawPIN, role, ttl) → stores digest + expiry
    ↓
VerifyAndUsePIN(rawPIN) → role (single-use, marks used_at)
    ↓
RedeemPIN(rawPIN) → generates persistent token, returns (rawToken, role)
```

PINs are consumed by `AgentAuthMiddleware` as fallback when bearer token validation fails. On redemption, the server returns the new token via `X-SSET-Token` response header.

## Admin Sessions

```
CreateAdminSession(rawSessionToken, ttl) → stored with expiry
    ↓
ValidateAdminSession(rawSessionToken) → checks expiry
```

Used by `AdminSessionMiddleware` (cookie-based auth for console).

## TOTP

`VerifyTOTP(secret, code)` validates time-based OTP codes for console login. Uses `pquerna/otp`.

## Token Generation

`GenerateToken()` produces URL-safe random tokens. `GeneratePIN()` produces short numeric PINs. `ComputeDigest()` hashes tokens with SHA-256 for storage.

## Rules
* Tokens are stored as SHA-256 digests — raw tokens are never persisted.
* Cache is read-through: first `ValidateToken` hits DB, subsequent reads serve from `sync.Map`.
* Revoked/expired tokens are evicted from cache on revocation.
