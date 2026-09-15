# Gate: System Shortcuts Passthrough

## Condition
When the command palette is closed and the user presses Meta+C, Meta+V, Meta+X, Meta+A, or other Meta+key combinations, the browser's native shortcuts (copy, paste, cut, select-all) are NOT prevented — i.e., `e.preventDefault()` is NOT called for these events. The events propagate to the browser naturally.

For the **cloud shell** handler: Meta+key events (where key is not Meta/Ctrl) must NOT call `e.preventDefault()` or `e.stopPropagation()`, allowing the browser to handle them natively.

For the **remote desktop** handler: Meta+key events must still be sent to the remote desktop via `handleDesktopKey`, but the palette must not intercept them. The `handleDesktopKey` function should still call `e.preventDefault()` for remote keys (since we want to send them to the remote, not let the browser act on them).

## Evidence Required
- [ ] Artifact 1: Shell handler code showing Meta+key events are NOT intercepted (no preventDefault/stopPropagation for non-meta keys when palette is closed) → `frontend/console/src/App.tsx`
- [ ] Artifact 2: Desktop handler code showing Meta+key events are forwarded to handleDesktopKey without palette interference → `frontend/console/src/App.tsx`

## Verification Method
Code review: verify that in the shell handler, when the palette is closed and the key is not a meta key, the handler returns early without calling preventDefault/stopPropagation. In the desktop handler, verify Meta+key combos are sent to handleDesktopKey (which forwards to remote) without the palette opening.

## Owner
Engineer
