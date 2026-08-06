# Remote App Input ACK & Deferred Capture

## Goal
When the agent receives any input event from the server, it sends an InputAck back so the console UI can display a live tooltip on the current screenshot; screenshot capture and upload are deferred until no input event has been received for at least 3 seconds.

## Context
The current signal-driven capture model (PR #37) captures immediately on action events and uses a 15s idle fallback. The server ACKs screenshots (FrameScreenshotAck) but there is no feedback from agent→server→frontend when input events are received. The frontend has no way to visually confirm that input events reached the agent.

### Current wire protocol:
- FrameScreenshot (0x01): Agent → Server [8-byte timestamp][JPEG]
- FrameInput (0x02): Server → Agent [JSON InputEvent]
- FrameScreenInfo (0x03): Agent → Server [JSON ScreenInfo]
- FrameLogEvent (0x04): Agent → Server [JSON LogEvent]
- FrameScreenshotAck (0x05): Server → Agent [8-byte timestamp]

### Current capture model:
- `captureNow` channel signals on action events (clicks, keys, scrolls)
- 15s idle timer fallback
- Mouse-move events don't trigger captures

## Success Criteria
1. **InputAck frame:** Agent sends a new `FrameInputAck` (0x06) back to server for every input event received, carrying the input event type so the server can forward it to the frontend.
2. **Frontend tooltip:** Server forwards InputAck to frontend as an SSE named event; frontend displays a brief tooltip overlay on the current screenshot (e.g., "click", "key:a", "scroll:up").
3. **Deferred capture:** Agent defers screenshot capture/upload until no input event has been received for at least 3 seconds. During active input, no screenshots are sent.
4. **Idle fallback:** After 3s of no input events, capture a screenshot and resume the idle timer cycle.
5. **Tests pass:** All existing tests continue to pass; new tests cover InputAck round-trip and deferred capture behavior.

## Constraints
- Must maintain backward compatibility within the same deployment (agent + server versioned together).
- InputAck must be lightweight (no large payloads).
- Tooltip must not block or interfere with input event delivery.
- The 3s deferral timer must be reset on every input event received.

## Out of Scope
- Changes to the frontend React SPA beyond what's needed for tooltip display.
- Changes to authentication or session management.
- Modifying the existing FrameScreenshotAck (0x05) protocol.

## Created
2026-07-31T00:00:00Z
