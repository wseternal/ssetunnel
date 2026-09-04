# Gate: Keyboard Integration

## Condition
The Meta key toggle and palette shortcut keys work correctly when xterm.js has focus. The palette does not interfere with terminal input when closed.

## Evidence Required
- [ ] Meta key handler registered on window (not xterm), works regardless of xterm focus → `frontend/console/src/App.tsx`
- [ ] When palette is open, shortcut keys (H, F, Q, Escape) are handled and xterm input is blocked → `frontend/console/src/App.tsx`
- [ ] When palette is closed, all keyboard events pass through to xterm normally → `frontend/console/src/App.tsx`
- [ ] Input/textarea/select focus guard present (palette doesn't trigger when typing in form fields) → `frontend/console/src/App.tsx`

## Verification Method
- Code review: Meta key handler on `window` level, not xterm `onKey`
- Code review: input/textarea/select focus guard present
- Build verification passes

## Owner
Engineer + Frontend Engineer
