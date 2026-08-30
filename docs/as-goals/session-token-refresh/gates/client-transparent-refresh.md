# Gate: Client Transparent Refresh

## Condition
Agent and connect client proactively refresh their session token before expiration without user intervention. The session file stores expiry metadata and triggers refresh when the remaining TTL falls below a configurable threshold. After refresh, the session file is updated with the new token.

## Evidence Required
- [ ] Artifact 1: SessionEntry includes `expires_at` field → `internal/auth/session_file.go`
- [ ] Artifact 2: Client-side refresh function → `internal/auth/session_file.go` or dedicated file
- [ ] Artifact 3: Agent integrates refresh on startup → `cmd/ssetunnel/main.go` or `internal/agent/`
- [ ] Artifact 4: Connect client integrates refresh on startup → `internal/connect/client.go`
- [ ] Artifact 5: Unit tests for session file with expiry → `internal/auth/session_file_test.go`
- [ ] Artifact 6: Unit tests for refresh logic → appropriate test file
- [ ] Artifact 7: Backward compatibility: sessions without `expires_at` still work (graceful degradation)

## Verification Method
1. Run unit tests: `go test ./internal/auth/... -v -timeout 30s`
2. Run connect tests: `go test ./internal/connect/... -v -timeout 30s`
3. Verify session file JSON includes `expires_at` after save
4. Verify loading a session without `expires_at` does not error (backward compat)
5. Verify refresh is triggered when TTL < threshold
6. Verify refresh is NOT triggered when TTL > threshold
7. Verify session file is updated after successful refresh
8. Full suite: `go test ./... -timeout 120s`

## Owner
Senior Software Engineer
