# Gate: Duration Range Selector

## Condition
The per-agent metrics section includes a duration range selector offering 1h, 6h, 12h, 1d, and 7d. Selecting a different duration re-fetches samples with the corresponding `from`/`to` range. The selector is accessible and visually consistent with the existing MUI design system.

## Evidence Required
- [ ] Artifact 1: UI component (ToggleButtons or Select) with all 5 options → `frontend/console/src/App.tsx`
- [ ] Artifact 2: `onChange` handler that updates state and triggers re-fetch → `frontend/console/src/App.tsx`
- [ ] Artifact 3: Duration values mapped to time offsets (1h=1h, 6h=6h, etc.) → `frontend/console/src/App.tsx`

## Verification Method
- Grep for the 5 duration options in the JSX
- Verify state change triggers `fetchAgentSamples` with new range
- Verify component renders in both admin and read-only statistics tabs

## Owner
Senior Software Engineer + Frontend Engineer (review)
