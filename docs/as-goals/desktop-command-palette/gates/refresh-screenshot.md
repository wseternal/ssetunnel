# Gate: Refresh Screenshot Protocol

## Condition
Selecting "Refresh Screenshot" from the command palette sends a control signal through the existing transport pipeline that causes the agent's CaptureLoop to immediately capture and send a screenshot, bypassing the 3-second defer timer. The control signal does NOT invoke robotgo or affect the local desktop.

## Evidence Required
- [ ] Artifact 1: New control event type (e.g., `refresh_screenshot`) added to the protocol validation → `internal/remoteapp/input_validation.go`, `internal/remoteapp/protocol.go`
- [ ] Artifact 2: Agent-side handling triggers immediate capture on receiving the control event → `internal/remoteapp/proxy.go`
- [ ] Artifact 3: CaptureLoop accepts an immediate-capture signal (new channel or control mechanism) → `internal/remoteapp/capture.go`
- [ ] Artifact 4: Frontend sends the control event via the existing POST /connect-up endpoint → `frontend/console/src/App.tsx`
- [ ] Artifact 5: Unit tests for the new control event type → `internal/remoteapp/input_validation_test.go`, `internal/remoteapp/protocol_test.go`

## Verification Method
- Code review: `refresh_screenshot` is added to `validInputTypes` whitelist
- Code review: `DispatchInput` ignores `refresh_screenshot` (no robotgo call)
- Code review: `ProxyRemoteApp` sends an immediate-capture signal to `CaptureLoop` when receiving `refresh_screenshot`
- Code review: `CaptureLoop` handles the immediate-capture signal by firing `captureAndSend()` without waiting for the defer timer
- Test: `ValidateInputEventType("refresh_screenshot")` returns true
- Test: `go test ./internal/remoteapp/... -timeout 30s` passes
- Test: `go test ./... -timeout 120s` passes (no regressions)

## Owner
Engineer (backend protocol) + Frontend Engineer (frontend trigger)
