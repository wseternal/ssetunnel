# Shell Command Palette

## Goal
Add a command palette overlay to the Cloud Shell page (matching the existing Remote Desktop command palette UX), replacing the old icon buttons (theme cycling and fullscreen toggle).

## Context
- The Remote Desktop page already has a command palette triggered by Meta key (Cmd on macOS, Ctrl on non-Mac) with actions: Refresh Screenshot, Send Text, Toggle Fullscreen, Disconnect.
- The Cloud Shell page has two icon buttons in its PageHeader: a theme cycling button (PaletteIcon) and a fullscreen toggle button (FullscreenIcon/FullscreenExitIcon).
- The shell and desktop pages are on separate tabs (mutually exclusive visibility), so a single Meta key trigger can safely target whichever page is active.
- The shell uses xterm.js for terminal input, which captures keyboard events. The Meta key handler must work above xterm.

## Success Criteria
- Cloud Shell page shows a command palette overlay when Meta key is pressed (same trigger as desktop).
- Command palette includes actions: Toggle Theme (H), Toggle Fullscreen (F), Disconnect (Q) — available when shell is connected.
- The old theme cycling and fullscreen toggle icon buttons are removed from the Cloud Shell PageHeader.
- Keyboard shortcuts work correctly when xterm.js has focus.
- The Agent selector and Connect/Disconnect buttons remain in the PageHeader (these are primary controls, not icon buttons).
- Frontend build succeeds (`bun run build` in `frontend/console/`).

## Constraints
- Must match the existing desktop command palette visual style (Paper overlay, Chip shortcuts, Typography).
- Shell palette must not interfere with xterm.js terminal input when palette is closed.
- Palette state must be cleared on shell disconnect.

## Out of Scope
- Backend changes (no API or protocol modifications).
- Changes to the desktop command palette behavior.
- Adding new actions beyond the three listed above.
- Modifying xterm.js configuration or behavior.

## Created
2026-09-04
