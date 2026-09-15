# Remote App Streaming

## Goal
The remote desktop panel gains a toggleable streaming mode: "Start Streaming" begins a continuous flow of WebP-encoded screenshots at max 5 FPS; the palette item then reads "Stop Streaming" and clicking it halts the stream, reverting to manual-refresh-only — with the entire screenshot pipeline (including the existing refresh action) switched from JPEG to WebP via `github.com/deepteams/webp`.

## Context
The remote app feature (`internal/remoteapp/`, `internal/server/remoteapp.go`, `frontend/console/src/App.tsx`) currently captures screenshots manually only: the command palette's "Refresh Screenshot" (R) action sends a `refresh_screenshot` input event through the server to the agent, which captures the primary display via robotgo, encodes as JPEG quality 75, and sends it back as a timestamped `FrameScreenshot` over the yamux stream; the server forwards it base64-encoded over SSE to the browser `<img>` element. There is no continuous capture mechanism (removed in commit 75320b2). No WebP library is currently a dependency. `github.com/deepteams/webp` is a pure-Go, zero-CGO WebP encoder/decoder requiring Go 1.24+ (project uses Go 1.26.5); it registers itself with `image.Decode` via `init()`.

## Success Criteria
- Command palette shows "Start Streaming" (shortcut S) when not streaming, "Stop Streaming" when streaming; clicking/pressing toggles the mode
- While streaming, the browser `<img>` updates continuously at ≤5 FPS with WebP frames
- The 5 FPS cap is a hard maximum — never exceeded even under force-refresh signals
- All screenshot frames (streaming and manual refresh) are WebP, encoded agent-side with `github.com/deepteams/webp`
- Frontend decodes WebP via `data:image/webp;base64,...` (browser-native decode)
- Streaming auto-stops on disconnect; reconnecting starts in non-streaming mode
- "Refresh Screenshot" (R) remains available during streaming (triggers an immediate extra frame within the rate cap)
- `go build ./...` and `go vet ./...` clean; focused tests pass

## Constraints
- Max 5 FPS hard cap on streaming captures
- Use `github.com/deepteams/webp` for encoding (agent) — pure Go, no CGO
- Toggle lives in the command palette with shortcut "S", mirroring the existing "Refresh Screenshot" pattern
- Streaming state is per-connection (agent-side capture loop + frontend toggle), not persisted
- Intra frames only (each frame independently encoded)
- Preserve existing wire protocol frame types where possible; protocol changes must be version-tolerant or documented as breaking (agent and server already must run same version per existing timestamp-prefix breaking change)

## Out of Scope
- Inter-frame/delta encoding or keyframe schemes
- Adaptive FPS based on measured bandwidth
- WebSocket transport (stay on SSE + POST)
- Persisting streaming preference across sessions
- Changes to the input (mouse/keyboard) path
- Server-side WebP decoding (server forwards opaque payload)

## Created
2026-09-15
