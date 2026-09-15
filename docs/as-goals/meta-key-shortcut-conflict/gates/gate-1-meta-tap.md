# Gate: Meta-Key-Tap Detection

## Condition
The command palette opens ONLY when the Meta key (Cmd on macOS, Ctrl on non-Mac) is pressed and released without any other key being pressed during the hold. If the user presses Meta+C, Meta+V, or any other Meta+key combination, the palette does NOT open.

## Evidence Required
- [ ] Artifact 1: Refactored keyboard handler in App.tsx showing meta-key-tap logic (keydown sets flag, other keys mark "not a tap", keyup toggles palette) → `frontend/console/src/App.tsx`
- [ ] Artifact 2: Both desktop and shell handlers implement the same tap detection pattern → `frontend/console/src/App.tsx`

## Verification Method
Code review: verify that the palette toggle no longer happens on Meta keydown but on Meta keyup, gated by a "otherKeyPressed" flag. Verify the flag is reset on Meta keydown and checked on Meta keyup.

## Owner
Engineer
