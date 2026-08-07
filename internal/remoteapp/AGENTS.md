# Remote App

Screen capture and input replay for remote desktop control over a yamux stream. Uses robotgo for screen capture and input dispatch (supports both CGo and pure-Go backends).

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
| `FrameScreenshot` | 0x01 | Agent → Server | [8-byte BE UnixMilli timestamp][JPEG data (quality 50)] |
| `FrameInput` | 0x02 | Server → Agent | JSON `InputEvent` |
| `FrameScreenInfo` | 0x03 | Agent → Server | JSON `ScreenInfo{width,height}` |
| `FrameLogEvent` | 0x04 | Agent → Server | JSON `LogEvent{ts,sev,src,msg}` |
| `FrameScreenshotAck` | 0x05 | Server → Agent | 8-byte BE UnixMilli (ACK for received screenshot) |
| `FrameInputAck` | 0x06 | Agent → Server | JSON `InputAck{type,detail}` |

> **Breaking change:** `FrameScreenshot` payload includes an 8-byte timestamp prefix. `FrameInputAck` (0x06) is a new frame type. Agent and server must run the same version — mismatched versions will produce corrupt screenshots or unknown frame types during a rolling deployment.

Max frame size: 4 MiB (`maxFrameSize`). `WriteFrame` constructs the header and payload in two separate writes to avoid allocating a combined ~150 KB buffer. Callers MUST ensure exclusive access to the writer for the duration of a `WriteFrame` call; the two writes are NOT atomic. Use `lockedWriter` for concurrent access. `ReadFrame` reads header then payload with `io.ReadFull`.

## Build Constraints

- **Supported OS** (`darwin || windows || linux`): Includes robotgo-backed `capture.go` and `input.go`. Build with `-tags purego` for CGo-free compilation (see [robotgo CGo-free builds](https://github.com/go-vgo/robotgo#cgo-free-builds)). On darwin, `capture_darwin.go` (CGO, CoreGraphics) is used by default; with `-tags purego`, `capture_darwin_purego.go` (error-string matching) is used instead.
- **Unsupported OS** (e.g. freebsd, solaris): `capture_stub.go` and `input_stub.go` return `ErrNotSupported`. `Enabled()` returns false.

`input_validation.go` has no build constraint — pure validation logic testable without robotgo.

## Capture Loop

`CaptureLoop(ctx, w, inputReceived <-chan struct{})` captures the primary display and writes timestamped JPEG screenshots as typed frames. It uses a **deferred-capture strategy**:

- **`inputReceived` channel**: Every input event signals this channel, resetting a 3-second deferral timer. Buffered 1 for coalescing.
- **3-second deferral timer (`deferDelay`)**: Capture only fires after no input events have been received for 3 seconds. While the user is actively interacting, screenshots are suppressed to avoid uploading immediately-stale frames.
- **Initial capture**: One screenshot on startup before entering the select loop, so the frontend receives the first frame immediately.
- **Buffer reuse**: Single `bytes.Buffer` reused across frames (~150 KB/frame savings).
- **Circuit breaker**: After `maxConsecutiveCaptureFails` (10) consecutive non-transient failures, returns an error.
- **Display-unavailable backoff**: When `isDisplayUnavailable()` returns true (monitor off/sleeping), the loop retries every 30 s (`displayOffBackoff`) instead of the 3 s deferral, resets `consecutiveFails`, and does NOT trip the circuit breaker. On macOS (CGO) this queries CoreGraphics `CGDisplayIsActive`; on other platforms (or darwin with `-tags purego`) it matches the robotgo error string (`robotgoCaptureErrSubstr`). Input events cancel active backoff.
- **JPEG quality**: 50 balances bandwidth (~50–150 KB per 1080p frame) and clarity.

Each screenshot payload is prefixed with an 8-byte big-endian Unix-millisecond timestamp (`ScreenshotTimestampSize = 8`). The server parses and strips this prefix before forwarding JPEG to the frontend, and sends a `FrameScreenshotAck` back to the agent.

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
1. Wrap stream in a `lockedWriter` for concurrent-safe frame writes
2. Send `FrameScreenInfo` with initial screen dimensions
3. Create `inputReceived` channel (buffered 1) and start `CaptureLoop` goroutine
4. Main loop: `ReadFrame` → dispatch input events; for every `FrameInput`, send `FrameInputAck` back with event type and detail; signal `inputReceived` on all input events (deferring capture); handle `FrameScreenshotAck` from server
5. On stream close: cancel capture, `ReleaseAllInputs`, wait for capture goroutine, emit "session ended" log event, close `lockedWriter`, close stream

## Concurrency Model

The agent→server direction has concurrent writers: the capture goroutine (screenshots + log events) and the main goroutine (input dispatch log events). A `lockedWriter` wraps the yamux stream with a `sync.Mutex` to serialize all frame writes:

- `Write(p []byte)` — mutex-guarded single write (satisfies `io.Writer` for non-aware callers like `WriteFrame`)
- `writeFrame(frameType, data)` — mutex held across header+data `WriteFrame` (atomic frame construction)
- `writeLogEvent(severity, message)` — build `LogEvent` JSON + `writeFrame` under mutex
- `writeScreenshotWithTimestamp(jpegData, ts)` — build timestamped payload + `writeFrame` under mutex
- `writeInputAck(ack InputAck)` — marshal `InputAck` JSON + `writeFrame` under mutex
- `close()` — set `closed=true` under mutex, preventing further writes

`CaptureLoop` detects `*lockedWriter` via type assertion to use mutex-guarded methods; otherwise falls back to bare `WriteFrame`/`WriteLogEvent` (caller must serialize).

## Log Event Flow

Agent-side `lockedWriter.writeLogEvent` and server-side `writeSSELogEvent` produce `LogEvent` JSON frames:

1. Agent emits log events for: session start/stop, capture start/stop, errors
2. Server validates agent log events: enforce `src=agent`, validate `sev` ∈ {info, warn, error}, cap `msg` at 1024 chars
3. Server injects its own events: stream opened, stream closed, agent session died
4. Server forwards validated events as SSE `event: log` frames to the console frontend
5. Frontend renders log entries with severity-based coloring and auto-scroll

## Input Acknowledgment Flow

When the agent receives a `FrameInput` from the server, it sends a `FrameInputAck` back containing the event type and a brief detail string. The server forwards this as an SSE `event: inputack` named event to the console frontend, which displays a live tooltip overlay on the current screenshot. This provides immediate visual feedback that the agent received the user's input, even before the next screenshot arrives (which is deferred by 3 seconds while input is flowing).

## Rules
* **lockedWriter for concurrent writes**: All agent→server frame writes go through a `lockedWriter` that serializes access with a mutex. The capture goroutine and main goroutine share the same `lockedWriter`.
* **Fail-fast**: Capture write errors, stream read errors, and robotgo panics terminate the session.
* **No partial recovery**: A dead stream means a new session — agents do not attempt to recover mid-stream.
* **robotgo is blocking**: All robotgo calls block the calling goroutine. Input dispatch runs in the main loop (serial), not a goroutine, to avoid input reordering.
