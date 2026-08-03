# Remote App

Screen capture and input replay for remote desktop control over a yamux stream. Uses robotgo for CGo-backed screen capture and input dispatch.

## Architecture

```
Server (SSE bridge)
  │  yamux stream (typed frames)
  ▼
ProxyRemoteApp(stream)        ← agent side
  ├── WriteFrame(ScreenInfo)  ← initial screen dimensions
  ├── CaptureLoop(ctx)        ← goroutine: screenshots → stream
  └── ReadFrame loop          ← main: stream → DispatchInput()
```

## Wire Protocol

Typed length-prefixed frames: `[type][4-byte BE length][data]`.

| Type | ID | Direction | Payload |
|---|---|---|---|
| `FrameScreenshot` | 0x01 | Agent → Server | JPEG image (quality 50) |
| `FrameInput` | 0x02 | Server → Agent | JSON `InputEvent` |
| `FrameScreenInfo` | 0x03 | Agent → Server | JSON `ScreenInfo{width,height}` |

Max frame size: 4 MiB (`maxFrameSize`). `WriteFrame` constructs the full frame in a single buffer and writes atomically to prevent interleaving. `ReadFrame` reads header then payload with `io.ReadFull`.

## Build Requirements

This package requires the robotgo CGo dependency. `input_validation.go` contains pure validation logic testable without robotgo.

## Capture Loop

`CaptureLoop(ctx, w, fps)` captures the primary display at `fps` (default 3) and writes JPEG-encoded screenshots as typed frames.

- **Buffer reuse**: Single `bytes.Buffer` reused across frames (~150 KB/frame savings).
- **Circuit breaker**: After `maxConsecutiveCaptureFails` (10) consecutive capture failures, returns an error instead of logging indefinitely.
- **JPEG quality**: 50 balances bandwidth (~50–150 KB per 1080p frame) and clarity.

## Input Dispatch

`DispatchInput(event, screenW, screenH)` maps JSON `InputEvent` to robotgo calls:

| Event Type | robotgo Call | Validation |
|---|---|---|
| `mouse_move` | `Move(x,y)` | Coords clamped |
| `mouse_click` | `Move+Click` | Coords clamped, button mapped |
| `mouse_scroll` | `ScrollDir(amt,dir)` | Amount capped [1,20], direction validated |
| `mouse_drag` | `Toggle+Move` | Coords clamped, button mapped |
| `key_tap` | `KeyTap(key,[mods])` | Key+modifiers whitelisted |
| `key_toggle` | `KeyToggle(key,state)` | Key whitelisted, state ∈ {down,up} |
| `type_text` | `Type(text)` | Length ≤ 256, no control chars |

`ReleaseAllInputs()` releases all mouse buttons and modifier keys — called on session teardown to prevent stuck keys from lost "up" events.

## Input Validation (no build tag)

`input_validation.go` exports pure functions testable without robotgo:
- `ValidateKeyEvent(key, mods)` — whitelist check for key and modifier names
- `ValidateText(text)` — length cap (256) and control character rejection
- `ValidateKeyToggleState(state)` — must be "down" or "up"
- `ValidateScrollAmount(amount)` — clamp to [1, 20], default 3
- `ValidateScrollDirection(dir)` — must be up/down/left/right, default "down"
- `clampCoords(x, y, w, h)` — clamp to [0, w) / [0, h)
- `mapButton(btn)` — map to robotgo's button names
- `sanitizeModifiers(mods)` — filter to valid modifier names

Typed error types: `InvalidKeyError`, `InvalidModifierError`, `TextTooLongError`, `ControlCharError`, `InvalidStateError`.

## Agent-Side Proxy

`ProxyRemoteApp(stream net.Conn)` orchestrates:
1. Send `FrameScreenInfo` with initial screen dimensions
2. Start `CaptureLoop` goroutine
3. Main loop: `ReadFrame` → dispatch input events
4. On stream close: cancel capture, `ReleaseAllInputs`, close stream, wait for capture goroutine

## Rules
* **Single writer per direction**: Only the capture loop writes screenshots to the stream; only the main loop reads input frames. No concurrent writers in the same direction.
* **Fail-fast**: Capture write errors, stream read errors, and robotgo panics terminate the session.
* **No partial recovery**: A dead stream means a new session — agents do not attempt to recover mid-stream.
* **robotgo is blocking**: All robotgo calls block the calling goroutine. Input dispatch runs in the main loop (serial), not a goroutine, to avoid input reordering.
