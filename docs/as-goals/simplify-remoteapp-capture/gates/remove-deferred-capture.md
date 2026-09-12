# Gate: Remove Deferred Capture

## Condition
The deferred capture mechanism (3-second defer timer, `inputReceived` channel, periodic re-capture after quiet period) is completely removed from the agent-side capture loop. No automatic screenshots are sent after the initial session-start capture.

## Evidence Required
- [ ] `capture.go` no longer contains `deferDelay`, `deferTimer`, or `inputReceived` channel logic
- [ ] `CaptureLoop` function signature no longer accepts `inputReceived` parameter
- [ ] `proxy.go` no longer creates or signals `inputReceived` channel
- [ ] Initial capture on session start still works (first screenshot sent immediately)
- [ ] Existing tests pass; new/updated tests verify no automatic re-capture

## Verification Method
- Code review: confirm all deferred capture code paths removed
- Test run: `go test ./internal/remoteapp/... -v` passes
- Test run: full `go build ./...` compiles cleanly

## Owner
Engineer
