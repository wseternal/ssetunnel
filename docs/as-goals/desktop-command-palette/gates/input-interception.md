# Gate: Input Interception

## Condition
Pressing the meta key (Cmd on macOS, Ctrl on other platforms) toggles the command palette open/closed. While the palette is open, ALL keyboard and mouse events are intercepted — no events are forwarded to the remote agent. Menu items can be triggered by click or shortcut key. Escape closes the palette. Clicking outside the palette closes it.

## Evidence Required
- [ ] Artifact 1: Meta key detection in the desktop keyboard handler → `frontend/console/src/App.tsx`
- [ ] Artifact 2: Input interception logic (palette open → suppress agent forwarding) → `frontend/console/src/App.tsx`
- [ ] Artifact 3: Escape and click-outside dismissal handlers → `frontend/console/src/App.tsx`

## Verification Method
- Code review: keyboard handler detects meta key press (keydown with metaKey/ctrlKey) and toggles palette state
- Code review: while palette is open, handleDesktopMouse and handleDesktopKey return early (no sendDesktopInput calls)
- Code review: Escape key closes palette; backdrop click closes palette
- Code review: shortcut keys (r, f, q) trigger corresponding actions when palette is open

## Owner
Engineer + Frontend Engineer (bench)
