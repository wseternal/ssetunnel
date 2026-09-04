# Gate: Shell Command Palette UI

## Condition
A command palette overlay renders on the Cloud Shell page when Meta key is pressed (Cmd on macOS, Ctrl on non-Mac), displaying three actions with keyboard shortcuts, matching the desktop palette visual style.

## Evidence Required
- [ ] Command palette JSX with Toggle Theme (H), Toggle Fullscreen (F), Disconnect (Q) actions → `frontend/console/src/App.tsx`
- [ ] Meta key handler attaches when `shellConnected` is true → `frontend/console/src/App.tsx`
- [ ] Palette overlay renders with Paper, Chip shortcuts, Typography (matching desktop style) → `frontend/console/src/App.tsx`
- [ ] Palette state cleared on shell disconnect → `frontend/console/src/App.tsx`

## Verification Method
- Code review: palette JSX matches desktop pattern (Paper overlay, Chip shortcuts)
- Code review: Meta key handler present in shell-connected effect
- Build verification: `cd frontend/console && bun run build` succeeds

## Owner
Engineer + Frontend Engineer
