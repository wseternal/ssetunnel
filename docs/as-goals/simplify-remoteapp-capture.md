# Simplify Remote App Capture

## Goal
Simplify the remote desktop screenshot pipeline to only capture on session start and manual refresh, and move the Activity Log from a bottom panel to a collapsible top-left overlay.

## Context
The remote app currently uses a deferred-capture strategy: a 3-second timer resets on every input event, and captures fire when input goes quiet. After each capture, the timer resets, creating a self-perpetuating periodic capture cycle. This adds complexity (defer timer, input-received signaling, backoff logic) and bandwidth for marginal benefit. The Activity Log renders as a separate panel below the remote desktop viewer, taking up vertical space.

## Success Criteria
- No automatic captures occur after the initial session-start capture
- Screenshots are only sent when: (a) first connected, (b) manual refresh action
- The `inputReceived` channel and defer timer are removed entirely
- The `forceCapture` channel is retained (manual refresh depends on it)
- Activity Log renders as a semi-transparent overlay in the top-left corner of the remote desktop viewer
- Activity Log has a toggle button to collapse/expand
- All existing tests pass; no regressions in input dispatch, screen info, input ack, or metrics flows

## Constraints
- No changes to the wire protocol or frame types
- No changes to input dispatch, screen info flow, input ack flow, or metrics polling
- The `forceCapture` channel must remain functional

## Out of Scope
- Changes to input dispatch or validation
- Changes to screen info flow
- Changes to input ack flow
- Changes to metrics polling
- Changes to the wire protocol or frame types

## Created
2026-09-12
