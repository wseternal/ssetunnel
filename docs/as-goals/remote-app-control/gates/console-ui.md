# Gate: Console UI

## Condition
The console web UI has a "Remote Desktop" tab that displays the agent's live screen image and captures mouse/keyboard input when connected.

## Evidence Required
- [ ] "Remote Desktop" tab in console tabs → `frontend/console/src/App.tsx`
- [ ] Agent selector dropdown (reuses connected agents list) → same file
- [ ] Connect/Disconnect button with session lifecycle → same file
- [ ] Image/canvas display showing live screenshot updates → same file
- [ ] Mouse event capture (click, scroll, drag) with coordinate translation → same file
- [ ] Keyboard event capture (keydown, keyup) with key combo detection → same file
- [ ] Disconnect cleanup (abort controller, event listener removal) → same file
- [ ] Frontend build: `bun run build` succeeds in `frontend/console/`

## Verification Method
1. Code review: verify tab component exists with agent selector
2. Code review: verify SSE connection for screenshot stream
3. Code review: verify POST calls for input events
4. Code review: verify cleanup on disconnect/unmount
5. Build: `bun run build` succeeds
6. Visual: tab renders without console errors

## Owner
Engineer
