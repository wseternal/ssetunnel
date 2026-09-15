# Review — Iteration 1

## Commit: `dfefa5b`
**Message:** fix(frontend): use meta key tap gesture to avoid conflict with system shortcuts

## Code Quality Findings

### Architecture & Correctness
- **PASS**: The meta-key-tap detection pattern is correctly implemented in both handlers. The logic flow is clean: meta keydown → set flags, non-meta keydown → mark non-tap, meta keyup → toggle if tap.
- **PASS**: The `metaOtherKeyRef` is shared between handlers, which is safe because only one handler is active at a time (guarded by `tabIndexRef.current`).
- **PASS**: Both cleanup functions reset `metaDownRef` and `metaOtherKeyRef` on unmount.

### Edge Cases Verified
- **PASS**: Meta tap to open → shortcut key → Meta tap to close: palette opens on first tap, handles shortcut, closes on second tap.
- **PASS**: Meta held + multiple keys (e.g., Cmd+A then Cmd+C): `metaOtherKeyRef` stays true, palette never opens.
- **PASS**: Palette open + Meta tap: palette closes correctly (toggle on keyup).
- **PASS**: Tab switch while meta is held: handler cleanup resets refs; new handler starts fresh.

### Shell Handler — System Shortcut Passthrough
- **PASS**: When meta is held and another key is pressed (e.g., Cmd+C), the handler sets `metaOtherKeyRef = true` and returns **without** calling `preventDefault()` or `stopPropagation()`. The event propagates to the browser naturally, allowing Cmd+C (copy), Cmd+V (paste), etc.
- **PASS**: The early `return` also prevents xterm from processing the Meta+key combination, which is correct — xterm would otherwise misinterpret Meta+C as an escape sequence.

### Desktop Handler — Remote Forwarding
- **PASS**: When meta is held and another key is pressed (e.g., Cmd+C), the handler sets `metaOtherKeyRef = true` and falls through to `handleDesktopKey`, which sends the key combination to the remote desktop. The palette does not open.
- **PASS**: `handleDesktopKey` calls `e.preventDefault()` for all remote keys, which is correct for remote desktop (keys should go to the remote, not the browser).

### Comments & Documentation
- **PASS**: Shell handler doc comment updated to explain the tap gesture pattern.
- **PASS**: Inline comments clearly explain the tap detection logic in both handlers.

## Summary
- **Critical:** 0
- **Warning:** 0
- **Suggestion:** 0

All changes are correct, well-commented, and handle edge cases properly.
