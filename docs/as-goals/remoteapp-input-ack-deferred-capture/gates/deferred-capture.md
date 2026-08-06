# Gate: Deferred Capture

## Condition
Agent defers screenshot capture and upload until no input event has been received for at least 3 seconds. During active input, no screenshots are sent. After 3s idle, a screenshot is captured and the idle timer cycle resumes.

## Evidence Required
- [ ] CaptureLoop uses a 3s deferral timer (replaces the current signal-driven immediate capture + 15s idle)
- [ ] Every input event resets the 3s deferral timer
- [ ] After 3s with no input events, a screenshot is captured
- [ ] During rapid input events, no screenshots are sent (verified by test)
- [ ] Unit test verifies the deferred capture behavior

## Verification Method
Run `go test -race ./internal/remoteapp/...` — deferred capture behavior verified by unit tests. Code review confirms timer reset on every input event.

## Owner
Engineer
