# Gate: Icon Button Removal

## Condition
The old theme cycling icon button (PaletteIcon) and fullscreen toggle icon button (FullscreenIcon/FullscreenExitIcon) are removed from the Cloud Shell PageHeader. The Agent selector and Connect/Disconnect button remain.

## Evidence Required
- [ ] No `<IconButton>` elements in `renderShellPanel` → `frontend/console/src/App.tsx`
- [ ] Agent selector (`<Select>`) and Connect/Disconnect button remain in PageHeader → `frontend/console/src/App.tsx`
- [ ] No unused icon imports (PaletteIcon, FullscreenIcon, FullscreenExitIcon) if no longer used elsewhere → `frontend/console/src/App.tsx`

## Verification Method
- Grep for `<IconButton` within renderShellPanel — zero matches
- Verify Agent selector and Connect/Disconnect button remain
- Build verification passes

## Owner
Engineer
