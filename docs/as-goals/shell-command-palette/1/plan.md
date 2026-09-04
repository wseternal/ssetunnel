# Plan — Iteration 1: Shell Command Palette

## File
`frontend/console/src/App.tsx` (single file — all changes in one component)

## Task 1: Add shell palette state
- Add `const [shellPaletteOpen, setShellPaletteOpen] = useState(false);` near existing palette state (line ~321)
- Add `useEffect` to sync a `shellPaletteOpenRef` if needed for keyboard handler access

**Acceptance:** State variable exists, palette defaults to closed.

## Task 2: Add shell keyboard handler
- Add a new `useEffect` with dep `[shellConnected]` that:
  - Registers `keydown` handler on `window` (same pattern as desktop handler at line ~1096)
  - Meta key toggle (Cmd on Mac, Ctrl on non-Mac) toggles `shellPaletteOpen`
  - When palette is open: Escape closes, H→cycleShellTheme, F→toggleShellFullscreen, Q→disconnectShell
  - Includes input/textarea/select focus guard
  - Registers `keyup` handler to reset `metaDownRef`
  - Returns cleanup that removes listeners

**Acceptance:** Meta key toggles palette when shell connected; shortcuts dispatch actions; no interference with xterm when palette closed.

## Task 3: Add shell palette action handler
- Add `handleShellPaletteAction` callback:
  ```ts
  const handleShellPaletteAction = useCallback((actionId: string) => {
    setShellPaletteOpen(false);
    switch (actionId) {
      case 'toggle-theme': cycleShellTheme(); break;
      case 'toggle-fullscreen': toggleShellFullscreen(); break;
      case 'disconnect': disconnectShell(); break;
    }
  }, [cycleShellTheme, toggleShellFullscreen, disconnectShell]);
  ```

**Acceptance:** Callback dispatches correct actions and closes palette.

## Task 4: Add shell palette overlay JSX
- Inside `renderShellPanel`, after the Paper (terminal container), add palette overlay:
  - Condition: `shellPaletteOpen && shellConnected`
  - Structure: Box (absolute, inset:0, z-index:20, backdrop) > Paper > header + 3 items + footer
  - Items: Toggle Theme (T), Toggle Fullscreen (F), Disconnect (Q)
  - Footer: "Press Esc to close • ⌘/Ctrl to toggle"
  - Visual style matches desktop palette exactly

**Acceptance:** Palette renders when open, matches desktop style, all 3 actions shown with shortcut chips.

## Task 5: Remove old icon buttons
- In `renderShellPanel`, remove the two `<Tooltip><IconButton>` blocks (theme cycling and fullscreen toggle) from the PageHeader actions Box
- Keep Agent selector (`<FormControl><Select>`) and Connect/Disconnect button
- Remove `PaletteIcon`, `FullscreenIcon`, `FullscreenExitIcon` imports ONLY if not used elsewhere

**Acceptance:** No `<IconButton>` in `renderShellPanel`; Agent selector and Connect/Disconnect remain; build passes.

## Task 6: Clear palette state on disconnect
- In `disconnectShell` callback, add `setShellPaletteOpen(false);`
- In the shell SSE disconnect handler (where `setShellConnected(false)` is called), add `setShellPaletteOpen(false);`

**Acceptance:** Palette auto-closes on shell disconnect.

## Risks
- Meta key event may propagate to xterm.js textarea, causing spurious escape sequences. Mitigation: `e.preventDefault()` on Meta keydown in handler.
- `renderShellPanel` is called twice (admin tabs and non-admin tabs) — both instances share the same `shellPaletteOpen` state, so only the visible tab's palette renders (the other is CSS-hidden).

## Rejected Alternatives
1. **Shared palette state between desktop and shell:** Rejected because they're independent features on different tabs with different action sets. Coupling them creates fragile conditional logic.
2. **Reusable CommandPalette component:** Rejected because the action sets differ significantly (desktop has Refresh Screenshot + Send Text; shell has Toggle Theme). Extracting a generic component adds complexity for only two usages.
