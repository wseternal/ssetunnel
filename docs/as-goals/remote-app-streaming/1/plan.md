# Plan — Iteration 1

**Goal:** Toggleable WebP screenshot streaming (≤5 FPS) in the remote app, whole pipeline switched from JPEG to WebP via `github.com/deepteams/webp`.

**Gates addressed:** streaming-toggle, continuous-capture-fps-cap, webp-pipeline, session-lifecycle (all four — first iteration builds the full feature).

---

## Architecture Decisions

### A1. Streaming control flows over the existing input-event channel
Start/stop streaming are new control event types (`start_streaming`, `stop_streaming`) added to the `validInputTypes` whitelist in `input_validation.go`, intercepted in `ProxyRemoteApp` exactly like `refresh_screenshot` — no new frame types, no server changes beyond what already exists (the server's `handleRemoteAppUp` validates via `remoteapp.ValidateInputEventType`, so adding to the whitelist is sufficient; the server stays payload-agnostic). This mirrors the established `refresh_screenshot` pattern end-to-end.

### A2. Streaming state lives in the agent's capture loop
`CaptureLoop` gains a `streaming <-chan bool` channel (buffered 1). The proxy's main goroutine sends `true` on `start_streaming`, `false` on `stop_streaming` (non-blocking, latest-wins via drain-then-send). The capture loop selects on: `ctx.Done()`, `forceCapture`, `streaming`, and a `*time.Ticker` channel that is only non-nil while streaming. This keeps all capture timing in one goroutine — no extra goroutines, no mutexes, and teardown is the existing `cancel()` + `wg.Wait()` path (satisfies the no-goroutine-leak rule and session-lifecycle gate by construction).

### A3. 5 FPS cap via ticker + coalesced force-refresh
- Streaming ticks: `time.NewTicker(200ms)` created on streaming start, stopped on stop. Each tick = one capture. Hard cap by construction (ticker can't fire faster than its period).
- Manual refresh during streaming: `forceCapture` signals are folded into the tick schedule — a force signal while streaming triggers a capture only if ≥200 ms elapsed since the last sent frame; otherwise it is dropped (documented behavior: stream is already delivering frames). When NOT streaming, force capture behaves exactly as today (immediate).
- Implementation: track `lastFrameAt time.Time`; a shared `maybeCapture(force bool)` helper enforces `time.Since(lastFrameAt) >= 200ms` when `force=true` during streaming.

### A4. WebP replaces JPEG everywhere in the capture path
- `capture.go`: `webp.Encode(&buf, img, &webp.EncoderOptions{Quality: 75, Method: 4})` replaces `jpeg.Encode`. `Method: 4` (default) is the speed/compression trade-off midpoint; encode time must be validated in Task 3 measurement — if p50 encode >100 ms on a 1080p frame, drop to `Method: 2` (documented fallback in the task).
- Constant rename: `jpegQuality` → `webpQuality = 75`; log messages updated ("jpeg encode" → "webp encode").
- Frontend: `data:image/jpeg;base64,` → `data:image/webp;base64,` (one line, `App.tsx:962`). Browser decodes natively — no JS decoder needed.
- Server: unchanged (forwards opaque base64 payload; `ParseScreenshotTimestamp` is format-agnostic).
- `internal/remoteapp/AGENTS.md` wire-protocol doc updated (JPEG → WebP) per project convention.

### A5. Frontend streaming state
- New state: `desktopStreaming` (bool) + `desktopStreamingRef` (for the keyboard handler, mirroring `paletteOpenRef`).
- Palette item: `{ id: 'toggle-streaming', label: desktopStreaming ? 'Stop Streaming' : 'Start Streaming', shortcut: 'S' }` inserted after Refresh Screenshot in the palette list (`App.tsx:2188`). Label is computed at render time — palette re-renders on open since `paletteOpen` is state.
- `handlePaletteAction('toggle-streaming')`: sends `{ type: desktopStreaming ? 'stop_streaming' : 'start_streaming' }` via `sendDesktopInput`, then `setDesktopStreaming(prev => !prev)` (optimistic toggle; the inputack tooltip already provides agent confirmation feedback).
- Keyboard: `case 's': handlePaletteAction('toggle-streaming')` in the palette-open switch (`App.tsx:1178`).
- Reset: `setDesktopStreaming(false)` in `resetDesktopState` (`App.tsx:~855`) — covers disconnect, reconnect, and SSE error paths (session-lifecycle gate).
- Optional but included: a small "LIVE" chip overlay (reusing the tooltip Chip pattern) while streaming, so the mode is visible without opening the palette. Low cost, clear UX win. (If it complicates the diff, it can be deferred — marked optional in task.)

### A6. No protocol version concern
No wire-format changes: frame types unchanged, screenshot payload remains `[8-byte timestamp][image bytes]` — only the image codec changes. Agent and server already must run the same version (existing timestamp-prefix breaking change), and the server never inspects image bytes. Frontend and Go binary ship together via `go:embed`, so MIME-prefix mismatch is impossible.

---

## Tasks (ordered, WIP = 1)

### Task 1 — Add `github.com/deepteams/webp` dependency
- **Files:** `go.mod`, `go.sum`
- **Do:** `go get github.com/deepteams/webp@latest` then `go mod tidy`. Verify version requires Go ≤1.26.5 and adds zero transitive deps.
- **Acceptance:** `go build ./...` clean; `go list -m github.com/deepteams/webp` resolves.

### Task 2 — RED: streaming + WebP characterization tests
- **Files:** `internal/remoteapp/capture_test.go` (extend), possibly new `internal/remoteapp/streaming_test.go`
- **Do (tests first, they will fail to compile until Task 3):**
  1. `TestStreamingTickerCapsFPS` — drive the capture loop with a fake capture function (see note) and a `streaming` channel; assert ≥200 ms between sent frames over ~1.1 s of streaming (expect ≤6 frames).
  2. `TestStreamingStopsCleanly` — start streaming, stop, wait 500 ms, assert zero frames after stop.
  3. `TestForceCaptureCoalescedDuringStreaming` — while streaming, fire 5 rapid force signals; assert no burst above the cap.
  4. `TestStreamingStopsOnContextCancel` — cancel ctx mid-stream; assert loop exits and no further frames.
  5. `TestWebPEncodeRoundTrip` — encode a synthetic `image.NRGBA` via the production encode helper; decode via `webp.Decode`; assert dimensions match and RIFF/WEBP magic present.
  6. `TestValidateInputEventType` — extend table with `start_streaming` / `stop_streaming` = true.
- **Note on testability:** `captureAndSend` currently calls `robotgo.CaptureImg` directly. Introduce a package-level `var captureImg = robotgo.CaptureImg` seam (same pattern as typical Go test seams) so tests can substitute a synthetic image source. This is the minimal seam; do NOT refactor the loop into an interface hierarchy.
- **Acceptance:** tests compile-fail or fail (RED) before Task 3; all pass (GREEN) after.

### Task 3 — GREEN: agent streaming + WebP implementation
- **Files:** `internal/remoteapp/capture.go`, `internal/remoteapp/proxy.go`, `internal/remoteapp/input_validation.go`, `internal/remoteapp/input.go`, `internal/remoteapp/protocol.go` (ackDetail table only)
- **Do:**
  1. `input_validation.go`: add `start_streaming`, `stop_streaming` to `validInputTypes` with comments.
  2. `capture.go`: signature `CaptureLoop(ctx, w, forceCapture <-chan struct{}, streaming <-chan bool)`; implement A2/A3 (ticker lifecycle, `lastFrameAt`, `maybeCapture`); swap JPEG→WebP (`webpQuality = 75`, `Method: 4`); add `var captureImg = robotgo.CaptureImg` seam; update log strings.
  3. `proxy.go`: create `streaming := make(chan bool, 1)`; pass to `CaptureLoop`; intercept `start_streaming`/`stop_streaming` like `refresh_screenshot` (drain-then-send latest-wins, `InputAck` with detail "streaming started"/"streaming stopped"); extend `ackDetail`.
  4. `input.go`: add no-op dispatch cases for the two control events (mirroring `refresh_screenshot` at `input.go:94`).
  5. Measure encode time: temporary benchmark or timed log on a real 1080p capture; if p50 >100 ms, set `Method: 2` with a comment. Record the number in the commit message / evidence.
- **Acceptance:** Task 2 tests green; `go build ./... && go vet ./...` clean; `go test ./internal/remoteapp/... -timeout 30s` green; `go build -tags purego ./...` clean (stub path unaffected but verify).

### Task 4 — Stub parity for purego/unsupported builds
- **Files:** `internal/remoteapp/capture_stub.go`, `internal/remoteapp/capture_purego.go` (if it re-declares CaptureLoop — check), `internal/remoteapp/capture_other.go` (no CaptureLoop expected)
- **Do:** update `CaptureLoop` signature in stubs to match Task 3 (accept the `streaming` channel, ignore it). Keep `Enabled()` semantics unchanged.
- **Acceptance:** `go build -tags purego ./...` and `GOOS=freebsd go build ./...` clean.

### Task 5 — Frontend toggle + WebP rendering
- **Files:** `frontend/console/src/App.tsx`, rebuilt `frontend/console/dist/`
- **Do:** implement A5 (state, palette item with dynamic label, `s` shortcut, `handlePaletteAction` case, reset in `resetDesktopState`, optional LIVE chip) and A4's one-line MIME switch (`App.tsx:962`).
- **Acceptance:** `cd frontend/console && npx tsc --noEmit` clean (or vite build typecheck); `npx vite build` succeeds; `dist/` rebuilt and included in the commit.

### Task 6 — Docs + full verification
- **Files:** `internal/remoteapp/AGENTS.md` (wire protocol table: JPEG→WebP; capture-loop section: streaming behavior; input table: new control events), root `AGENTS.md` only if it mentions JPEG (check — likely not)
- **Do:** update docs; run `go test ./internal/remoteapp/... ./internal/server/... -timeout 60s`; confirm clean.
- **Acceptance:** suites green; docs match implementation.

### Task 7 — Browser verification (Frontend Engineer evidence)
- **Do:** run the app locally (`./local.sh server` + agent with remoteapp enabled), connect desktop, verify: palette shows "Start Streaming" → activate (click and S) → frames update continuously (~5/s by observation) → label now "Stop Streaming" → stop → frames halt → R still refreshes → disconnect while streaming → reconnect → streaming off. Verify WebP via devtools (img src prefix `data:image/webp`).
- **Acceptance:** evidence notes recorded for the Test Engineer's manifest. If no second machine/display is available for a real agent, record that explicitly and verify as much as possible with a local agent on the dev machine.

---

## Commit plan (conventional commits)
1. `feat(remoteapp): add deepteams/webp dependency`
2. `test(remoteapp): add streaming and webp characterization tests` (RED)
3. `feat(remoteapp): add 5fps streaming mode and switch screenshots to webp` (GREEN, tasks 3+4)
4. `feat(frontend): add streaming toggle to remote desktop palette` (task 5)
5. `docs(remoteapp): document streaming and webp pipeline` (task 6)

## Risks / watch-items
- **WebP encode speed:** pure-Go VP8 is slower than libwebp; `Method: 4` on 1080p may approach the 200 ms budget. Mitigation: `Method: 2` fallback documented in Task 3; Performance Engineer validates.
- **Ticker + force channel interplay:** keep the logic inside the single capture goroutine; do not add a second timer goroutine.
- **Optimistic frontend toggle:** if the agent rejects the control event (e.g., old agent), the UI would show wrong state. Accepted: agent/server/frontend ship versioned together; inputack tooltip surfaces agent confirmation.
- **Test seam scope:** `var captureImg` only — resist broader refactors.
