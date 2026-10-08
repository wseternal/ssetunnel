# Fix: Cloud shell vim input hang

**Cycle start:** 2026-10-08 (autonomous)
**Branch:** main
**Base ref:** github/main
**Complexity:** Standard (bug fix with root-cause established; touches 1 file)
**Path:** Full path (Diagnose-and-Fix mode)

## Phase checklist

| Phase | Status | Exit gate |
|-------|--------|-----------|
| 0     | done   | github/main, tree clean, 0 behind, 0 ahead; Standard (bug-fix) |
| 1     | done   | RCA produced; plan approved by user |
| 2     | pending | implement + tests + build + `go test -race` pass |
| 3     | pending | review findings resolved; build clean |
| 4     | pending | simplification pass |
| 5     | pending | commit + recap + post-cycle choice |

## Root Cause Analysis (RCA)

**Symptom:** After executing `vim` in the remote cloud shell, all key inputs have no feedback. Vim appears to "hang".

**Reproduction:** Connect cloud shell → run `vim` → press keys → no character echo / no mode change / no cursor movement visible.

**Root cause:**
The frontend shell input handler in `frontend/console/src/App.tsx` (lines ~682-713) batches keystrokes via `requestAnimationFrame` before sending them as a POST to `/console/api/v1/shell/connect-up`. This was introduced in commit `2fcc0c5` (PR #67) to fix connection-pool saturation.

Two problems remain, which together produce the observed hang:

1. **Frame-rate coupling:** `requestAnimationFrame` is tied to the browser's rendering pipeline. During heavy terminal output (vim's full-screen redraws generating large SSE frames), the browser is busy decoding base64 and driving xterm.js rendering. rAF callbacks are coalesced with paint and can be delayed well beyond one 16 ms frame — input sits in `shellInputPendingRef` until the browser next enters the rendering phase.

2. **POST tail-reschedule gap:** After `flushShellInput`'s POST resolves, it re-checks `shellInputPendingRef.current` and *only* schedules a follow-up rAF if non-empty. Any keystroke that lands while the rAF callback is queued but before it fires is merged correctly; however, if the POST itself blocks on the browser's HTTP connection pool (HTTP/1.1 has 6 concurrent per origin; one is the SSE stream, others may be metrics polling / session listing / etc.), the rAF-driven flush can stall for tens to hundreds of ms. Combined with the next rAF waiting for a paint slot, the user perceives total unresponsiveness.

In contrast, bash at a prompt produces negligible output per keystroke, so rAF fires promptly and input feels fine. Vim's redraws change the timing regime entirely.

**Evidence:**
- `frontend/console/src/App.tsx:710-712` — rAF-only scheduling gate: `if (shellInputPendingRef.current === data) { requestAnimationFrame(flushShellInput); }`
- `frontend/console/src/App.tsx:702-704` — post-POST re-schedule uses rAF too.
- `frontend/console/src/App.tsx:789-810` — SSE reader loop processes base64-decoded frames synchronously in a tight `while (true)` loop, which can delay the next rendering tick.
- Commit `2fcc0c5` confirms the original fix targeted connection-pool saturation with rAF, but did not address the frame-rate coupling or POST serialization.

**Discarded hypotheses:**
- *Server-side inputForwarder blocked on yamux write*: ruled out — `writeMu` is held only for the duration of one `stream.Write`, and resizeForwarder shares the lock symmetrically; neither can starve the other.
- *Agent `forwardStreamToPTY` NUL-resize path swallowing input*: ruled out for vim — xterm.js does NOT emit `\x00` for Ctrl+Space (it sends no data for that combo), so the NUL-resize branch is not triggered by vim keymaps.
- *SSE writer starved / detached*: ruled out — the screenshot shows vim rendered fully, meaning the output path (PTY → agent → stream → server → SSE → browser) worked at startup.
- *Shell palette capture-phase listener swallowing keys*: ruled out — the capture handler (`App.tsx:1359-1401`) only calls `stopPropagation` for meta keys and palette shortcuts; all other keys propagate to xterm normally.
- *Auth token expiry mid-session*: ruled out — `flushShellInput` does not call `checkAuth`, but if the token were expired, the SSE stream would also fail and the user would see `[Connection closed]`.

## Plan

**Task 1 — Reproduction test (frontend):** Add a unit-level test in `frontend/console/` that exercises the new sendInput logic under load (simulated heavy SSE output) and verifies every input byte is delivered to the mock POST endpoint in order and without loss. If Vitest is not yet configured, add a lightweight node-runnable test file.

**Task 2 — Fix (App.tsx):** Replace `requestAnimationFrame` batching with a serialized-immediate sender:
- Remove `shellInputPendingRef`.
- `sendInput` enqueues data into a local buffer.
- A single-flight `flush` function: if a POST is in flight, set a `queued` flag and return; on POST completion, if `queued`, call `flush()` again.
- Send the first keystroke immediately (no frame delay); subsequent keystrokes while a POST is in flight are batched into the next POST.
- Keep `trackLineInput` (exit/logout/Ctrl-D detection) and HTTP error reporting intact.
- Clean up `shellInputPendingRef.current = ''` in the SSE finally block is removed (ref gone).

**Task 3 — Verification:** `go build ./...`, `go vet ./...`, `go test -race ./... -timeout 120s`; frontend `bun run build` (and `bun test` if configured).

**Risks:**
- Under very rapid typing (>60 keys/sec) the serialized sender issues one POST per round-trip rather than per-frame batching. This is acceptable: typical vim usage is one key at a time, waiting for output.

**Rejected alternatives:**
- *Keep rAF but add `setTimeout(0)` as a fast-path*: still couples to the rendering pipeline on the fallback path; adds complexity for marginal gain.
- *Use `navigator.sendBeacon`*: does not support custom `Authorization` headers required by the connect-up endpoint.
- *Open a dedicated WebSocket for input*: large architectural change; out of scope for a targeted fix.
