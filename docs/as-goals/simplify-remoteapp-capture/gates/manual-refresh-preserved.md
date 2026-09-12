# Gate: Manual Refresh Preserved

## Condition
The `forceCapture` channel and manual refresh action remain fully functional. When the user triggers "Refresh Screenshot" from the command palette, a new screenshot is captured and sent immediately.

## Evidence Required
- [ ] `forceCapture` channel still exists in `CaptureLoop` and is handled with priority
- [ ] `proxy.go` still creates `forceCapture` channel and signals it on `refresh_screenshot` events
- [ ] Frontend refresh action still sends the correct input event
- [ ] Tests verify force capture bypasses any backoff

## Verification Method
- Code review: confirm forceCapture path is intact
- Test run: `go test ./internal/remoteapp/... -v -run Force` passes

## Owner
Engineer
