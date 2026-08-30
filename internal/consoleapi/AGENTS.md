# Console API

JSON management API for the admin console. Mounted at `/api/v1/` under the console HTTP server (stripped of `/console` prefix).

## Routes

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/api/v1/user-login` | Public | Username/password login with per-user TOTP + recovery code fallback |
| POST | `/api/v1/user-login-check` | Public | Pre-login TOTP enrollment check (anti-enumeration) |
| POST | `/api/v1/logout` | Public | Clears session cookie, revokes user session |
| GET | `/api/v1/me` | User | Validate current session, return user info |
| POST | `/api/v1/refresh-session` | User | Atomically rotate session token (validate→create→delete); rate-limited per token |
| GET | `/api/v1/sessions` | User | List live tunnel sessions (admin: all, user: own only) |
| GET | `/api/v1/connected-agents` | User | List connected agent IDs (admin: all, user: own only) |
| GET | `/api/v1/agents` | User | List agent configs (non-admin: allowed_targets redacted) |
| POST | `/api/v1/agents` | Admin | Create agent config |
| PATCH/DELETE | `/api/v1/agents/{id}` | Admin | Update or delete agent config |
| GET/POST | `/api/v1/users` | Admin | List or create users |
| PATCH/DELETE | `/api/v1/users/{id}` | Admin | Update (role, password, permissions, disable) or delete user |
| GET | `/api/v1/totp/status` | User | TOTP enrollment status + remaining recovery codes |
| POST | `/api/v1/totp/begin-setup` | User | Generate TOTP secret + key URL |
| POST | `/api/v1/totp/verify-setup` | User | Verify TOTP code, persist secret + generate recovery codes |
| DELETE | `/api/v1/totp` | User | Clear TOTP + recovery codes (requires password confirmation) |
| GET | `/api/v1/metrics/overview` | User | Global metrics summary (scoped to visible agents) |
| GET | `/api/v1/metrics/agents` | User | Per-agent current metrics with snapshot + params + last decision |
| GET | `/api/v1/metrics/agents/{agentID}/samples` | User | Historical metric samples (query: `from`, `to` RFC3339) |
| GET | `/api/v1/metrics/agents/{agentID}/decisions` | User | Historical tuning decisions (query: `limit`) |

## Key Endpoints

### `POST /api/v1/user-login`
Username/password authentication with:
1. Password rate limiting (10 failures per IP per 5 min window)
2. Per-user TOTP verification (if enrolled) with recovery code fallback
3. TOTP rate limiting (5 failures per IP:username per 5 min window)
4. Creates a 30-day user session token; response includes `expires_at` (RFC 3339)

### `POST /api/v1/user-login-check`
Returns `{"totp_required": true/false}`. Returns `true` for non-existent users to prevent username enumeration. Fails closed (returns `true`) on DB errors.

### `POST /api/v1/totp/verify-setup`
Atomically persists TOTP secret + 8 recovery codes in a single transaction. Returns plaintext recovery codes to the user.

### `DELETE /api/v1/totp`
Requires password confirmation. Atomically clears TOTP secret and recovery codes.

### `GET /api/v1/sessions`
Admin sees all sessions; non-admin users see only sessions attributed to their own user ID.

### `GET /api/v1/connected-agents`
Returns deduplicated agent IDs from the live session registry. Scoped by user.

### Metrics Endpoints
All metrics endpoints are user-scoped: non-admin users see only agents from their own sessions. The `MetricsCollector` may be nil (metrics disabled); endpoints return empty arrays in that case.

## Login Rate Limiting

- **Password**: Per-IP, 10 failures within 5 min → 429 Too Many Requests.
- **TOTP**: Per-IP:username, 5 failures within 5 min → 429 Too Many Requests.
- Successful login resets the password rate limit for that IP.
- Successful TOTP resets the TOTP rate limit for that IP:username.
- Periodic cleanup goroutine purges expired entries every 10 min.

## Rules
* Protected routes use `AdminSessionMiddleware` or `UserSessionMiddleware` (bearer token).
* Login uses username/password authentication with per-user TOTP (not global).
* Logout revokes the server-side session via `RevokeUserSession`.
* The NULL default agent config row cannot be deleted or renamed.
* Non-admin users cannot see `allowed_targets` on agent configs.
* CORS middleware echoes back the origin only when it matches the request host (same-origin restriction).
* Password minimum length: 8 characters. Valid roles: `admin`, `user`.
