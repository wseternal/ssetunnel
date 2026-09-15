# Evidence Manifest — Iteration 1

## Gate Status

| Gate | Status | Evidence | Owner |
|------|--------|----------|-------|
| streaming-toggle | ✅ Pass | `frontend/console/src/App.tsx:335-337` (state), `:1071-1073` (handler), `:2201` (palette item), `internal/remoteapp/proxy.go:142-162` (control events), `internal/remoteapp/input_validation.go:194-195` (whitelist), `TestStreamingTickerCapsFPS` + `TestStreamingStopsCleanly` | Engineer |
| continuous-capture-fps-cap | ✅ Pass | `internal/remoteapp/capture.go:155-209` (ticker lifecycle), `TestStreamingTickerCapsFPS` (≥150ms intervals), `TestStreamingStopsCleanly` (no frames post-stop), `TestStreamingStopsOnContextCancel` (clean exit), `TestForceCaptureCoalescedDuringStreaming` (no burst) | Engineer |
| webp-pipeline | ✅ Pass | `go.mod` (deepteams/webp v1.2.7), `internal/remoteapp/capture.go:119` (`webp.Encode`), grep confirms zero `image/jpeg` import in `internal/remoteapp/`, `frontend/console/src/App.tsx:968` (`data:image/webp;base64,`), `TestWebPEncodeRoundTrip` (RIFF/WEBP magic + decode), `go build ./...` clean, `go build -tags purego ./...` clean | Engineer |
| session-lifecycle | ✅ Pass | `frontend/console/src/App.tsx:874-875` (`setDesktopStreaming(false)` in resetDesktopState), `internal/remoteapp/capture.go:171-176` (ticker.Stop on ctx.Done), `TestStreamingStopsOnContextCancel` (loop exits on cancel), `go test ./internal/remoteapp/... ./internal/server/... -timeout 60s` all green | Engineer |

## Return Shipments (Failed Gates)

None.

## Code Quality Findings

- Critical: 0
- Warning: 0
- Suggestion: 0

## Commits Reviewed

- `df317f4`: feat(remoteapp): add deepteams/webp dependency
- `3696b4d`: feat(remoteapp): add 5fps streaming mode and switch screenshots to webp
- `fd47541`: feat(frontend): add streaming toggle to remote desktop palette
- `efc6fb1`: docs(remoteapp): document streaming and webp pipeline
