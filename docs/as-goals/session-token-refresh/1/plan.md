# Plan — Iteration 1

## Architecture Decision

**Approach: Atomic token rotation via a dedicated refresh endpoint.**

- **Server:** `POST /api/v1/refresh-session` on the console API router. Takes the current valid bearer token, atomically creates a new session (30-day TTL) and deletes the old session. Returns `{ token, expires_at }`.
- **Store:** `RefreshUserSession(ctx, rawToken, ttl)` — single transaction: validate → create → delete old → return new info.
- **Session file:** Add `expires_at` (RFC 3339) and `console_url` fields to `SessionEntry`. Update `SaveSession`/`LoadSession` to include expiry data. Backward compatible: old sessions without `expires_at` still load (no refresh attempted).
- **Client integration:** After `LoadSession`, check if token is within 7 days of expiry. If so, call refresh endpoint using the stored `console_url` (derived from login server URL). On success, update session file and use new token. On failure, log warning and continue with current token.
- **Login response:** Add `expires_at` to the JSON response of `handleUserLogin`.
- **Security:** Refresh endpoint requires a valid, non-expired session token (UserSessionMiddleware pattern). Disabled users are rejected. Rate-limited (shares the TOTP rate limiter pattern — 10 refreshes per hour per user). Old token is atomically deleted, preventing replay.

## Task 1: Store — RefreshUserSession method

**File:** `internal/auth/store.go`

Add `RefreshUserSession(ctx, rawToken string, ttl time.Duration) (*UserSessionInfo, error)`:
1. Begin transaction
2. Validate current token (same query as `ValidateUserSession`): check digest, expiry, user not disabled
3. Generate new token
4. Create new session row with `expires_at = now + ttl`
5. Delete old session row (by old digest)
6. Commit
7. Return `UserSessionInfo` with new token's `ExpiresAt`

**Acceptance:** Unit test in `internal/auth/store_test.go`:
- Refresh with valid token → new token + new expires_at
- Old token is invalid after refresh (replay protection)
- Refresh with expired token → `ErrInvalidSession`
- Refresh with disabled user → `ErrInvalidSession` (or `ErrUserDisabled`)

## Task 2: Console API — refresh-session endpoint

**File:** `internal/consoleapi/router.go`

Add `POST /api/v1/refresh-session`:
1. Extract bearer token
2. Call `store.RefreshUserSession(ctx, token, 30*24*time.Hour)`
3. On success: return `{ "token": newToken, "expires_at": expiresAtRFC3339 }`
4. On failure: return 401

Register route with `userAuth` middleware (same as `/api/v1/me`).

**Acceptance:** Unit test in `internal/consoleapi/consoleapi_test.go`:
- Refresh with valid session → 200 + new token
- Old token rejected after refresh → 401
- Disabled user refresh → 401
- Expired token refresh → 401

## Task 3: Login response — include expires_at

**File:** `internal/consoleapi/router.go`

In `handleUserLogin`, the `CreateUserSession` already computes `expiresAt` internally but doesn't return it. Modify the flow:
1. Compute `expiresAt := time.Now().UTC().Add(30*24*time.Hour)` before calling `CreateUserSession`
2. Add `"expires_at": expiresAt.Format(time.RFC3339)` to the login response JSON

**Acceptance:** Existing login tests still pass. The response JSON includes `expires_at`.

## Task 4: Session file — add expires_at and console_url fields

**File:** `internal/auth/session_file.go`

1. Add `ExpiresAt string` and `ConsoleURL string` to `SessionEntry` (JSON: `expires_at`, `console_url`, both `omitempty`)
2. Update `SaveSession` signature: `SaveSession(serverURL, token, username, role, consoleURL string, expiresAt time.Time) error`
   - Format `expiresAt` as RFC 3339, store `consoleURL`
3. Update `LoadSession` return: `(token, resolvedServer, consoleURL string, expiresAt time.Time, err error)`
   - Parse `expires_at` from RFC 3339; return zero time if empty (backward compat)
4. Update all callers of `SaveSession` and `LoadSession`

**Acceptance:** Unit test in `internal/auth/session_file_test.go`:
- Save with expires_at → Load returns correct time
- Load old-format session (no expires_at) → zero time, no error
- ConsoleURL round-trips correctly

## Task 5: Client-side refresh function

**File:** `internal/auth/session_file.go` (or new `internal/auth/refresh.go`)

Add `RefreshSession(serverURL, consoleURL, currentToken string) (newToken string, newExpiresAt time.Time, err error)`:
1. `POST <consoleURL>/console/api/v1/refresh-session` with `Authorization: Bearer <currentToken>`
2. Parse response: `{ "token": "...", "expires_at": "..." }`
3. Return new token and parsed expires_at
4. On non-200: return error with status code and body

**Acceptance:** Unit test with `httptest.Server` returning mock response.

## Task 6: CLI integration — proactive refresh

**File:** `cmd/ssetunnel/main.go`

1. Update `runLogin` to pass `consoleURL` and `expiresAt` to `SaveSession`
2. Update `resolveServerURL` to:
   - Load session including expiry data via updated `LoadSession`
   - If `expiresAt` is non-zero and within 7 days of now, call `RefreshSession`
   - On success: update session file via `SaveSession`, use new token
   - On failure: log warning, continue with current token (don't block operation)
3. Update `runAgent` and `runConnect` to pass the console URL through to the request modifier (no change needed — they just use the token from `resolveServerURL`)

**Acceptance:**
- Agent/connect with fresh token (< 7 days to expiry): no refresh call
- Agent/connect with aging token (> 7 days to expiry but > 0): refresh called, session updated
- Agent/connect with expired token: refresh fails (401), clear error message
- Agent/connect with no expires_at (old session): no refresh attempted (backward compat)

## Task 7: Tests and verification

1. Run `go vet ./...`
2. Run `go test ./internal/auth/... -v -timeout 30s`
3. Run `go test ./internal/consoleapi/... -v -timeout 120s` (needs testcontainer)
4. Run `go test ./... -timeout 120s` (full suite)
5. Fix any failures

## Task Order
1 → 2 → 3 → 4 → 5 → 6 → 7
