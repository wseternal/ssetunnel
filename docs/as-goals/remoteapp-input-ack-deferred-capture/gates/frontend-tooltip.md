# Gate: Frontend Tooltip

## Condition
Frontend displays a brief tooltip overlay on the current screenshot when an `inputack` SSE event is received. The tooltip shows the input event type (e.g., "click", "key", "scroll") and auto-dismisses after a short timeout. It does not block or interfere with input delivery.

## Evidence Required
- [ ] Frontend handles `inputack` SSE named events
- [ ] Tooltip component renders on the screenshot display area
- [ ] Tooltip shows the input event type from the InputAck payload
- [ ] Tooltip auto-dismisses (fade-out after ~1-2s)
- [ ] No layout shift or blocking behavior

## Verification Method
Code review of frontend component. Manual verification: trigger input events, confirm tooltip appears on screenshot and dismisses.

## Owner
Frontend Engineer
