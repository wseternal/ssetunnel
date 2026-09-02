# Gate: Manual Refresh, No Auto-Poll

## Condition
The 10-second `setInterval` that polls `fetchMetricsOverview` and `fetchAgentMetrics` is removed. A refresh icon button exists that manually triggers a data reload. The existing PageHeader Refresh button remains functional.

## Evidence Required
- [ ] Artifact 1: No `setInterval` for metrics polling in the main `useEffect` → `frontend/console/src/App.tsx`
- [ ] Artifact 2: Refresh icon button (IconButton with RefreshIcon) in the metrics section → `frontend/console/src/App.tsx`
- [ ] Artifact 3: Button onClick calls both `fetchMetricsOverview()` and `fetchAgentMetrics()` → `frontend/console/src/App.tsx`

## Verification Method
- Grep for `setInterval.*fetchMetricsOverview` — must NOT appear
- Grep for `setInterval.*fetchAgentMetrics` — must NOT appear
- Verify an IconButton with RefreshIcon exists near the "Per-Agent Metrics" heading
- Verify existing PageHeader Refresh button still works

## Owner
Senior Software Engineer
