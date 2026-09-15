# Gate: session-lifecycle

## Condition
Streaming state is per-connection and does not leak across sessions. When the browser disconnects (SSE close, yamux stream death, or explicit Disconnect), the agent's streaming capture stops. A new connection starts in non-streaming mode (frontend state reset). No goroutine leaks, no stuck captures, and `ReleaseAllInputs` / capture teardown behavior from the existing session-end path is preserved.

## Evidence Required
- [ ] Frontend resets streaming state on disconnect/connect → `frontend/console/src/App.tsx` line range
- [ ] Agent streaming stops on ctx cancellation / stream close (ctx-owned goroutine, per project no-leak rule) → file path + line range
- [ ] Test proving streaming does not survive session teardown → test file path + test name
- [ ] Existing session-end tests still pass (`go test ./internal/remoteapp/... ./internal/server/... -timeout 60s`) → command output in evidence manifest
- [ ] Browser-observed: disconnect while streaming, reconnect — palette shows "Start Streaming" and no frames arrive until manually refreshed (Frontend Engineer evidence) → description in evidence manifest

## Verification Method
Test Engineer runs the remoteapp and server test suites, inspects teardown paths, and reviews Frontend Engineer evidence. Pass requires: teardown tests green, state reset proven, no regression in existing session tests.

## Owner
Senior Software Engineer (agent/server) + Frontend Engineer (frontend state)
