# Gate: continuous-capture-fps-cap

## Condition
While streaming is active, the agent captures and sends screenshots continuously at a rate that never exceeds 5 FPS (minimum 200 ms between frame sends), regardless of additional force-refresh signals. When streaming stops, captures cease (no background frames). The capture loop still terminates cleanly on session end with no goroutine leaks.

## Evidence Required
- [ ] Streaming capture loop implementation in `internal/remoteapp/` (ticker or rate-limited loop integrated with existing `CaptureLoop`/`forceCapture`) → file path + line range
- [ ] Test proving frame-send intervals are ≥200 ms under streaming, including while forceCapture is signaled concurrently → test file path + test name
- [ ] Test proving captures stop after stop-streaming (no frames sent post-stop) → test file path + test name
- [ ] Test proving clean shutdown (goroutine exits on ctx cancel / stream close while streaming) → test file path + test name
- [ ] Performance Engineer measurement: observed frame intervals and any burst analysis → note in evidence manifest

## Verification Method
Test Engineer runs the timing tests (`go test ./internal/remoteapp/... -run Streaming -v -timeout 30s`) and inspects the rate-limiting mechanism. Performance Engineer reviews the timing evidence. Pass requires: hard cap proven by test, stop proven by test, clean shutdown proven by test.

## Owner
Senior Software Engineer; Performance Engineer validates measurements
