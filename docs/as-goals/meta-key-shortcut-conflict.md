# Fix Meta Key Conflict with System Shortcuts

## Goal
Users can use system shortcuts (Cmd+C, Cmd+V, Cmd+X, etc.) normally in both cloud shell and remote desktop views, while still being able to open the command palette by tapping the Meta key alone.

## Context
The command palette in both cloud shell and remote desktop views is triggered by pressing the Meta key (Cmd on macOS, Ctrl on non-Mac). Currently, the palette toggles on Meta **keydown**, which means pressing Cmd+C immediately opens the palette before the 'c' key is processed, preventing the browser's native copy shortcut from working. The same applies to Cmd+V (paste), Cmd+X (cut), Cmd+A (select all), etc.

The keyboard handlers are in `frontend/console/src/App.tsx`:
- Desktop keyboard handler (line ~1136-1201): uses capture phase, intercepts Meta key, toggles palette, sends all other keys to remote
- Shell keyboard handler (line ~1258-1315): uses capture phase, intercepts Meta key, toggles palette, lets xterm handle normal keys

## Success Criteria
- Cmd+C copies content in the browser (not intercepted by palette)
- Cmd+V pastes content in the browser (not intercepted by palette)
- Tapping Meta key alone (press and release without pressing other keys) opens the command palette
- Command palette shortcuts (R, T, F, Q while palette is open) still work correctly
- Remote desktop still receives modified key combinations (Ctrl+C, etc.) when palette is not triggered
- All existing tests pass, frontend builds successfully

## Constraints
- Must maintain capture-phase event handling (required for xterm.js v6 compatibility)
- Must not break the existing command palette UX (tap meta to open, press shortcut key)
- Must handle both macOS (Meta/Cmd) and non-Mac (Ctrl) meta key variants

## Out of Scope
- Changing the command palette trigger to a different key combination
- Adding user-configurable keyboard shortcuts
- Backend changes (this is frontend-only)

## Created
2026-09-15
