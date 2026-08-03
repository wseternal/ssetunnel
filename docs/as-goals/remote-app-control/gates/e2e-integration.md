# Gate: End-to-End Integration

## Condition
A complete connect → screenshot stream → input → disconnect cycle works end-to-end through the tunnel, with proper auth, session management, and cleanup.

## Evidence Required
- [ ] Connect session created with `__remote_app__` target → `internal/server/handlers.go`
- [ ] Agent routes `__remote_app__` target to screen capture handler → `internal/agent/agent.go`
- [ ] Session cleanup on disconnect (pipe close, stream close, session delete) → handlers.go
- [ ] Auth enforced on remote app endpoints → `internal/server/shell.go` or new file
- [ ] Route registration in console server → `internal/consoleserver/consoleserver.go`
- [ ] Existing tests still pass: `go test ./... -timeout 120s`
- [ ] Frontend dist rebuilt: `frontend/console/dist/index.html`

## Verification Method
1. Code review: verify `__remote_app__` target handling in agent proxy
2. Code review: verify connect session bridge for screenshot mode
3. Code review: verify auth middleware on new endpoints
4. Test: `go test ./... -timeout 120s` passes
5. Test: `bun run build` passes in frontend
6. Integration: manual connect/disconnect cycle works (if agent with display available)

## Owner
Engineer
