# Gate: Server Refresh Endpoint

## Condition
The server exposes a session refresh endpoint that accepts a valid, non-expired user session token and returns a new session token with a fresh 30-day TTL. The old token is invalidated after the client confirms the new token.

## Evidence Required
- [ ] Artifact 1: Refresh endpoint handler in consoleapi → `internal/consoleapi/router.go`
- [ ] Artifact 2: Store method for token rotation → `internal/auth/store.go`
- [ ] Artifact 3: Unit tests for refresh flow → `internal/consoleapi/consoleapi_test.go`
- [ ] Artifact 4: Unit tests for store rotation → `internal/auth/store_test.go`
- [ ] Artifact 5: Response includes `expires_at` for client-side expiry tracking

## Verification Method
1. Run unit tests: `go test ./internal/consoleapi/... -run Refresh -v`
2. Run auth store tests: `go test ./internal/auth/... -run Refresh -v`
3. Verify that a valid token returns a new token + new expires_at
4. Verify that an expired token is rejected with 401
5. Verify that a disabled user's token is rejected with 403
6. Verify that the old token is invalidated after refresh (replay protection)

## Owner
Senior Software Engineer
