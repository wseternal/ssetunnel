# Plan — Iteration 1

## Analysis

The root cause is that both the desktop and shell keyboard handlers toggle the command palette on Meta key **keydown**. This means pressing Cmd+C triggers:
1. Meta keydown → palette opens (immediately, before 'c' is pressed)
2. 'c' keydown → palette intercepts 'c' as a shortcut letter

The fix is to defer the palette toggle to Meta **keyup**, only triggering when no other key was pressed during the Meta hold (a "tap" gesture).

## Tasks

### Task 1: Add `metaOtherKeyRef` for tap detection

**File:** `frontend/console/src/App.tsx`
**Location:** After the existing `metaDownRef` declaration (~line 326)

Add a new ref `metaOtherKeyRef` that tracks whether any non-meta key was pressed during the current Meta key hold. This ref is shared between desktop and shell handlers (only one is active at a time based on tab index).

```typescript
const metaOtherKeyRef = useRef(false);
```

**Acceptance:** New ref declared alongside `metaDownRef`.

---

### Task 2: Refactor desktop keyboard handler (keydown)

**File:** `frontend/console/src/App.tsx`  
**Location:** Desktop keyboard handler inside useEffect (~line 1145-1185)

Change the Meta key handling from:
- **Before:** On Meta keydown → immediately toggle palette, preventDefault, stopPropagation
- **After:** On Meta keydown → set `metaDownRef = true`, reset `metaOtherKeyRef = false`, preventDefault, stopPropagation (but do NOT toggle palette)
- On non-meta key while `metaDownRef.current` is true (and palette is closed) → set `metaOtherKeyRef = true`, return (let `handleDesktopKey` handle it)
- On non-meta key while palette is open → handle palette shortcuts as before
- On non-meta key otherwise → `handleDesktopKey` as before

**Acceptance:** Desktop handler no longer toggles palette on keydown. Palette shortcuts still work when palette is open.

---

### Task 3: Refactor desktop keyboard handler (keyup)

**File:** `frontend/console/src/App.tsx`  
**Location:** Desktop keyUp handler inside useEffect (~line 1187-1192)

Change the Meta key keyup from:
- **Before:** Reset `metaDownRef = false`
- **After:** If `!metaOtherKeyRef.current` → toggle palette. Then reset both `metaDownRef = false` and `metaOtherKeyRef = false`.

**Acceptance:** Palette toggles on Meta keyup only when no other key was pressed during the hold.

---

### Task 4: Refactor shell keyboard handler (keydown)

**File:** `frontend/console/src/App.tsx`  
**Location:** Shell keyboard handler inside useEffect (~line 1266-1298)

Change the Meta key handling from:
- **Before:** On Meta keydown → immediately toggle palette, preventDefault, stopPropagation
- **After:** On Meta keydown → set `metaDownRef = true`, reset `metaOtherKeyRef = false`, preventDefault, stopPropagation (but do NOT toggle palette)
- On non-meta key while `metaDownRef.current` is true (and palette is closed) → set `metaOtherKeyRef = true`, return (let event propagate to xterm/browser)
- On non-meta key while palette is open → handle palette shortcuts as before
- Otherwise → let event propagate normally to xterm

**Acceptance:** Shell handler no longer toggles palette on keydown. Meta+key combos (Cmd+C, etc.) are NOT intercepted, allowing browser native shortcuts.

---

### Task 5: Refactor shell keyboard handler (keyup)

**File:** `frontend/console/src/App.tsx`  
**Location:** Shell keyUp handler inside useEffect (~line 1300-1305)

Same pattern as Task 3: On Meta keyup, if `!metaOtherKeyRef.current` → toggle shell palette. Reset both refs.

**Acceptance:** Shell palette toggles on Meta keyup only when no other key was pressed.

---

### Task 6: Build and verify

**Command:** `cd frontend/console && bun run build`

**Acceptance:** Build completes with exit code 0. The `frontend/console/dist/` files are updated.
