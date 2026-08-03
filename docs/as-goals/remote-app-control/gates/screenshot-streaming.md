# Gate: Screenshot Streaming

## Condition
The agent can capture its primary display as JPEG screenshots and stream them to the console browser at 2-5 FPS through the existing tunnel infrastructure.

## Evidence Required
- [ ] Agent-side screen capture module using robotgo → `internal/remoteapp/capture.go` (or similar)
- [ ] JPEG encoding of captured screenshots → same file or `internal/remoteapp/encode.go`
- [ ] Server endpoint serving screenshot SSE stream → modified `internal/server/handlers.go` or new handler
- [ ] Console frontend displays live screenshot image → `frontend/console/src/App.tsx`
- [ ] Test verifying screenshot capture + JPEG encoding → `internal/remoteapp/capture_test.go`
- [ ] Build passes: `go build ./...` succeeds

## Verification Method
1. Code review: verify robotgo `CaptureScreen()` or equivalent is called
2. Code review: verify JPEG encoding with quality parameter
3. Code review: verify SSE frame format for binary image data
4. Test: unit test for capture → encode pipeline
5. Build: `go build ./...` succeeds (including CGo for robotgo)

## Owner
Engineer
