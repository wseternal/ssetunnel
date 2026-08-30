# Gate: Security Validation

## Condition
The token refresh mechanism is secure: disabled users cannot refresh, revoked/expired tokens are rejected, the refresh endpoint is rate-limited, and old tokens are invalidated after rotation to prevent replay attacks.

## Evidence Required
- [ ] Artifact 1: Test that disabled user refresh returns 403 → `internal/consoleapi/consoleapi_test.go`
- [ ] Artifact 2: Test that expired token refresh returns 401 → `internal/consoleapi/consoleapi_test.go`
- [ ] Artifact 3: Test that old token is rejected after rotation → `internal/auth/store_test.go`
- [ ] Artifact 4: Rate limiting on refresh endpoint → `internal/consoleapi/router.go`
- [ ] Artifact 5: Security review findings documented → `docs/as-goals/session-token-refresh/1/review.md`

## Verification Method
1. Verify disabled user cannot refresh (test + code review)
2. Verify expired token is rejected (test + code review)
3. Verify old token is unusable after successful refresh (test + code review)
4. Verify rate limiting prevents refresh spam (code review)
5. Security Engineer review: no authorization bypass vectors
6. Security Engineer review: no indefinite session extension for compromised tokens

## Owner
Security Engineer (review) + Senior Software Engineer (implementation)
