# Evidence Manifest — Iteration 1

## Gate Status

| Gate | Status | Evidence | Owner |
|------|--------|----------|-------|
| G1: Server refresh endpoint | ✅ Pass | `internal/consoleapi/router.go` (handleRefreshSession), `internal/auth/store.go` (RefreshUserSession), `internal/consoleapi/consoleapi_test.go` (TestRefreshSession), `internal/auth/store.go` (RefreshResult type) | Engineer |
| G2: Client transparent refresh | ✅ Pass | `internal/auth/session_file.go` (SessionEntry fields, SaveSession/LoadSession), `internal/auth/refresh.go` (NeedsRefresh, RefreshSession), `cmd/ssetunnel/main.go` (resolveServerURL proactive refresh), `internal/auth/session_file_test.go` (TestSaveSession_ExpiresAt, TestLoadSession_BackwardCompat_NoExpiresAt), `internal/auth/refresh_test.go` (TestNeedsRefresh, TestRefreshSession_Success, TestRefreshSession_ServerError) | Engineer |
| G3: Security validation | ✅ Pass | `internal/consoleapi/consoleapi_test.go` (TestRefreshSession: disabled user rejected, old token rejected), `internal/auth/store.go` (FOR UPDATE lock, expires_at > CURRENT_TIMESTAMP, disabled_at IS NULL), `internal/consoleapi/router.go` (userAuth middleware) | Security Engineer + Engineer |

## Gate Evidence Detail

### G1: Server Refresh Endpoint
- ✅ `POST /api/v1/refresh-session` registered with `userAuth` middleware
- ✅ `Store.RefreshUserSession` performs atomic validate→create→delete in transaction
- ✅ Response includes `token` and `expires_at` (RFC 3339)
- ✅ `TestRefreshSession`: valid token → 200 + new token + expires_at
- ✅ Old token rejected after rotation (replay protection verified)
- ✅ `TestRefreshSession`: expired token → 401

### G2: Client Transparent Refresh
- ✅ `SessionEntry` includes `expires_at` and `console_url` fields (JSON omitempty)
- ✅ `SaveSession` accepts consoleURL and expiresAt parameters
- ✅ `LoadSession` returns 5 values including consoleURL and expiresAt
- ✅ `NeedsRefresh` returns true when < 7 days remaining, false for zero time
- ✅ `RefreshSession` calls server endpoint and parses response
- ✅ `resolveServerURL` integrates proactive refresh
- ✅ `runLogin` saves expires_at from login response
- ✅ Backward compat: old sessions without expires_at → zero time → no refresh
- ✅ All existing session file tests pass with updated signatures

### G3: Security Validation
- ✅ Disabled user refresh rejected (TestRefreshSession step 4)
- ✅ Expired token refresh rejected (SQL: `expires_at > CURRENT_TIMESTAMP`)
- ✅ Old token invalidated after rotation (atomic delete in transaction)
- ✅ FOR UPDATE row lock prevents concurrent rotation races
- ✅ userAuth middleware provides outer validation gate
- ✅ No authorization bypass vectors identified

## Code Quality Findings
- Critical: 0
- Warning: 0
- Suggestion: 1 (consider rate limiting on refresh endpoint — non-blocking)

## Commits Reviewed
- `b1746358`: feat(auth): add transparent session token refresh mechanism
