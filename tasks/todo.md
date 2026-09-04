# Send Text — Task Checklist

## Phase 1: Implementation

- [x] **Task 1**: Add "Send Text" menu item + text editor state
  - [x] Add `{ id: 'send-text', label: 'Send Text', shortcut: 'T' }` to palette items array
  - [x] Add `textEditorOpen` state and `textEditorContent` state
  - [x] Add `'send-text'` case in `handlePaletteAction`
  - [x] Add `'t'` shortcut in palette keyboard handler
  - [x] Add `setTextEditorOpen(false)` to `resetDesktopState`

- [x] **Task 2**: Implement multi-line text sending logic
  - [x] Add `sendTextToDesktop` function
  - [x] Split text by `\n`, send each line as `type_text`
  - [x] Send `key_tap: enter` between lines
  - [x] Chunk lines >256 bytes

- [x] **Task 3**: Render text editor overlay UI
  - [x] Overlay with backdrop (click to close)
  - [x] Paper card with header "Send Text"
  - [x] `<textarea>` with auto-focus, monospace font, dark theme
  - [x] Escape keydown → close without sending
  - [x] Cancel button → close without sending
  - [x] Send button → call `sendTextToDesktop` (disabled when empty)

## Checkpoint

- [x] `bun run build` succeeds
- [x] All acceptance criteria from Tasks 1-3 met
