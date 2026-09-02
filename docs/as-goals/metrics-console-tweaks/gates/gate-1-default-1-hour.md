# Gate: Default 1-Hour View

## Condition
When the Statistics tab loads, `fetchAgentSamples` passes `from` and `to` query parameters that restrict results to the most recent 1 hour. The initial state of the duration selector is "1h".

## Evidence Required
- [ ] Artifact 1: React state with default value "1h" → `frontend/console/src/App.tsx`
- [ ] Artifact 2: `fetchAgentSamples` constructs URL with `from`/`to` params based on selected duration → `frontend/console/src/App.tsx`

## Verification Method
- Grep for default state value of duration selector
- Grep for `from`/`to` query parameter construction in `fetchAgentSamples`
- Verify the URL defaults to 1-hour range

## Owner
Senior Software Engineer
