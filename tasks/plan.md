# Implementation Plan: Send Text (Command Palette)

## Overview
Add a "Send Text" action to the Remote Desktop command palette. When triggered, a text editor overlay appears where the user can compose multi-line text. Clicking "Send" sends the text to the remote desktop as a sequence of `type_text` + `key_tap: enter` events. Enter in the textarea adds a newline (default behavior); sending requires an explicit button click.

## Architecture Decisions

### 1. No backend changes needed
The existing `type_text` (with `ValidateText` 256-byte limit + control character rejection) and `key_tap: enter` events are sufficient. The frontend splits multi-line text into a sequence of events: `type_text` per line + `key_tap: enter` between lines. Long lines (>256 bytes) are chunked.

### 2. Two-phase UI (palette → text editor)
Selecting "Send Text" from the palette closes the palette and opens a text editor overlay in the same position. The text editor is a separate overlay (not embedded in the palette) because it needs a `<textarea>` element, which requires focus and must not have its keyboard events intercepted by the desktop handler.

### 3. Keyboard handling is automatic
The existing desktop keyboard handler already skips events when `e.target` is a `TEXTAREA` element (line 1017: `if (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT') return`). So typing in the textarea won't trigger meta-key toggle or desktop shortcuts.

### 4. Escape to dismiss
A dedicated keydown listener on the textarea handles Escape to close the editor without sending. This is the only keyboard shortcut the editor intercepts beyond normal textarea behavior.

## Task List

### Task 1: Add "Send Text" menu item + text editor state

**Description:** Add the "Send Text" action to the command palette menu items list and introduce state for the text editor overlay.

**Changes:**
- Add `{ id: 'send-text', label: 'Send Text', shortcut: 'T' }` to the palette items array (after "Refresh Screenshot")
- Add state: `const [textEditorOpen, setTextEditorOpen] = useState(false)`
- Add state: `const [textEditorContent, setTextEditorContent] = useState('')`
- In `handlePaletteAction`, add case `'send-text'`: sets `paletteOpen=false`, `textEditorOpen=true`, `textEditorContent=''`
- In the keyboard useEffect's palette shortcut switch, add `case 't': handlePaletteAction('send-text'); return;`
- In `resetDesktopState`, add `setTextEditorOpen(false)` for cleanup on disconnect

**Acceptance criteria:**
- [ ] "Send Text" appears in the command palette with shortcut "T"
- [ ] Pressing T while palette is open transitions to text editor state
- [ ] `textEditorOpen` and `textEditorContent` state exist

**Dependencies:** None  
**Files:** `frontend/console/src/App.tsx`  
**Scope:** XS (1 file, ~10 lines)

---

### Task 2: Implement multi-line text sending logic

**Description:** Add a function that sends multi-line text to the remote desktop by splitting on newlines and composing `type_text` + `key_tap: enter` events. Lines longer than 256 bytes are chunked.

**Changes:**
- Add `sendTextToDesktop` function:
  ```tsx
  const sendTextToDesktop = useCallback(() => {
    const text = textEditorContent;
    setTextEditorOpen(false);
    setTextEditorContent('');
    const sid = desktopSessionId;
    const abort = desktopAbortRef.current;
    if (!sid || !abort || !text) return;

    const lines = text.split('\n');
    for (let i = 0; i < lines.length; i++) {
      const line = lines[i];
      // Chunk long lines to fit ValidateText's 256-byte limit
      for (let j = 0; j < line.length; j += 256) {
        const chunk = line.slice(j, j + 256);
        sendDesktopInput(sid, { type: 'type_text', text: chunk }, abort.signal);
      }
      // Send Enter between lines (not after the last line)
      if (i < lines.length - 1) {
        sendDesktopInput(sid, { type: 'key_tap', key: 'enter' }, abort.signal);
      }
    }
  }, [textEditorContent, desktopSessionId, sendDesktopInput]);
  ```

**Acceptance criteria:**
- [ ] Single-line text sends as one `type_text` event
- [ ] Multi-line text sends each line as `type_text` with `key_tap: enter` between lines
- [ ] Lines >256 bytes are chunked into multiple `type_text` events
- [ ] Empty lines send just Enter (no `type_text` for empty string)
- [ ] Editor closes and content clears after sending

**Dependencies:** Task 1  
**Files:** `frontend/console/src/App.tsx`  
**Scope:** S (1 file, ~20 lines)

---

### Task 3: Render text editor overlay UI

**Description:** Render a text editor overlay when `textEditorOpen` is true. The overlay contains a `<textarea>` for multi-line input, a "Send" button, and a "Cancel" button. The textarea auto-focuses on open. Escape dismisses.

**Changes:**
- Render overlay inside the desktop Paper (same position as command palette, `zIndex: 20`)
- Contains:
  - Header: "Send Text" label
  - `<textarea>`: multi-line input, auto-focus, Escape handler, min-height ~150px, full width of the card
  - Footer: Cancel button (closes without sending) + Send button (calls `sendTextToDesktop`)
- Textarea styling: monospace font, dark background to match desktop theme, no resize handle
- Send button disabled when textarea is empty
- Escape keydown on textarea: `setTextEditorOpen(false)`
- Backdrop click: close editor (same as palette)

**Acceptance criteria:**
- [ ] Text editor overlay renders when `textEditorOpen` is true
- [ ] Textarea auto-focuses on open
- [ ] Enter adds newline (default textarea behavior)
- [ ] Escape closes the editor without sending
- [ ] Click outside (backdrop) closes without sending
- [ ] Send button triggers `sendTextToDesktop` and closes editor
- [ ] Cancel button closes without sending
- [ ] Send button is disabled when textarea is empty
- [ ] Frontend builds without errors

**Dependencies:** Task 1, Task 2  
**Files:** `frontend/console/src/App.tsx`  
**Scope:** S (1 file, ~50 lines JSX)

---

### Checkpoint: Complete
- [ ] `cd frontend/console && bun run build` succeeds
- [ ] Command palette shows 4 items: Refresh Screenshot, Send Text, Toggle Fullscreen, Disconnect
- [ ] Selecting "Send Text" (click or T shortcut) opens text editor
- [ ] Typing multi-line text and clicking Send delivers text to remote desktop
- [ ] Enter in textarea adds newline, does NOT send
- [ ] Escape closes text editor without sending
- [ ] Disconnect cleanup resets text editor state

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Text with control characters (tabs, etc.) rejected by ValidateText | Low | Tabs are control chars (`0x09`). If needed, expand ValidateText or replace tabs with spaces before sending. For now, document limitation. |
| Very large text (>50KB) sends many sequential POST requests | Low | Unlikely for manual text entry. No rate limiting needed since these are user-initiated. |
| Textarea steals focus from desktop after editor closes | Low | Explicitly blur textarea before closing, or rely on the keyboard handler to resume. |

## Open Questions
None — the scope is clear and self-contained.
