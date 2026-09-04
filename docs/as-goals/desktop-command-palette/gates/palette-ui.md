# Gate: Command Palette UI

## Condition
A command palette overlay component renders on top of the remote desktop view when triggered. It displays three menu items: "Refresh Screenshot" (r), "Toggle Fullscreen" (f), and "Disconnect" (q). The palette is visually consistent with the Mercury Console design system (MUI v9, light theme).

## Evidence Required
- [ ] Artifact 1: Palette component implemented in `frontend/console/src/App.tsx` (or extracted sub-component) → `frontend/console/src/App.tsx`
- [ ] Artifact 2: Frontend builds without errors → build output
- [ ] Artifact 3: Palette renders 3 menu items with correct labels and shortcut key hints → component code

## Verification Method
- Code review: palette component renders with correct MUI styling
- Build verification: `cd frontend/console && bun run build` succeeds
- All three menu items are present in the component with labels and shortcut hints

## Owner
Engineer + Frontend Engineer (bench)
