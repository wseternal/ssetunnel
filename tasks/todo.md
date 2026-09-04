# Send Text — Task Checklist

## Phase 1: Implementation

- [ ] **Task 1**: Add "Send Text" menu item + text editor state
  - [ ] Add `{ id: 'send-text', label: 'Send Text', shortcut: 'T' }` to palette items array
  - [ ] Add `textEditorOpen` state and `textEditorContent` state
  - [ ] Add `'send-text'` case in `handlePaletteAction`
  - [ ] Add `'t'` shortcut in palette keyboard handler
  - [ ] Add `setTextEditorOpen(false)` to `resetDesktopState`

- [ ] **Task 2**: Implement multi-line text sending logic
  - [ ] Add `sendTextToDesktop` function
  - [ ] Split text by `\n`, send each line as `type_text`
  - [ ] Send `key_tap: enter` between lines
  - [ ] Chunk lines >256 bytes

- [ ] **Task 3**: Render text editor overlay UI
  - [ ] Overlay with backdrop (click to close)
  - [ ] Paper card with header "Send Text"
  - [ ] `<textarea>` with auto-focus, monospace font, dark theme
  - [ ] Escape keydown → close without sending
  - [ ] Cancel button → close without sending
  - [ ] Send button → call `sendTextToDesktop` (disabled when empty)

## Checkpoint

- [ ] `bun run build` succeeds
- [ ] All acceptance criteria from Tasks 1-3 met
