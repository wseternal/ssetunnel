# Gate: Input Replay

## Condition
The console browser can send mouse events (click, scroll, drag) and keyboard events (text, key combos) to the agent, which replays them on the local desktop via robotgo.

## Evidence Required
- [ ] Input event protocol definition (JSON schema for mouse/keyboard events) → `internal/remoteapp/input.go`
- [ ] Agent-side input replay using robotgo → same file
- [ ] Server endpoint accepting input events via POST → `internal/server/handlers.go`
- [ ] Coordinate scaling: browser coordinates → agent screen coordinates → `internal/remoteapp/input.go` or frontend
- [ ] Input validation: coordinates within screen bounds, key names sanitized → `internal/remoteapp/input.go`
- [ ] Test verifying input parsing + dispatch → `internal/remoteapp/input_test.go`

## Verification Method
1. Code review: verify robotgo `MoveMouse()`, `MouseClick()`, `MouseScroll()`, `MouseToggle()`, `KeyTap()`, `TypeStr()` are called
2. Code review: verify coordinate scaling from browser viewport to agent screen dimensions
3. Code review: verify input validation (bounds check, key whitelist or sanitization)
4. Test: unit test for input event parsing and dispatch
5. Security review: verify no injection vectors in keyboard input

## Owner
Engineer
