# Iteration 1 — Plan

## Task 1: Add duration state and time-range helper

**File:** `frontend/console/src/App.tsx`

1. Add `ToggleButton` and `ToggleButtonGroup` to the MUI import (line 2-29).
2. Add state variable after line 268:
   ```ts
   const [metricsDuration, setMetricsDuration] = useState<string>('1h');
   ```
3. Add a helper function after the state declarations (around line 269):
   ```ts
   const durationToRange = (d: string): { from: string; to: string } => {
     const now = new Date();
     const hours: Record<string, number> = { '1h': 1, '6h': 6, '12h': 12, '1d': 24, '7d': 168 };
     const h = hours[d] ?? 1;
     const from = new Date(now.getTime() - h * 3600 * 1000);
     return { from: from.toISOString(), to: now.toISOString() };
   };
   ```

## Task 2: Modify `fetchAgentSamples` to accept duration

**File:** `frontend/console/src/App.tsx` (lines 420-427)

Replace `fetchAgentSamples` to pass `from`/`to` query params based on current `metricsDuration` state:
```ts
const fetchAgentSamples = async (agentID: string, duration?: string) => {
  try {
    const { from, to } = durationToRange(duration ?? metricsDuration);
    const params = new URLSearchParams({ from, to });
    const res = await fetch(`/console/api/v1/metrics/agents/${encodeURIComponent(agentID)}/samples?${params}`, { headers: authHeaders() });
    if (checkAuth(res) && res.ok) setSelectedAgentSamples(await res.json());
  } catch (e) {
    console.error(e);
  }
};
```

Add a `useEffect` to re-fetch when `metricsDuration` changes:
```ts
useEffect(() => {
  if (selectedStatsAgent) {
    fetchAgentSamples(selectedStatsAgent, metricsDuration);
  }
}, [metricsDuration]); // eslint-disable-line react-hooks/exhaustive-deps
```

## Task 3: Remove metrics auto-polling

**File:** `frontend/console/src/App.tsx` (lines 1255-1269)

Remove `metricsInterval` from the main useEffect:
- Remove `const metricsInterval = setInterval(...)` line
- Remove `clearInterval(metricsInterval)` from cleanup
- Keep the initial `fetchMetricsOverview()` and `fetchAgentMetrics()` calls
- Keep all other intervals (session, agent, connected)

## Task 4: Add duration selector and refresh button to admin statistics tab (tabIndex === 3)

**File:** `frontend/console/src/App.tsx` (around line 2051)

Replace the "Per-Agent Metrics" heading with a header row containing:
- Duration selector (`ToggleButtonGroup`)
- Refresh `IconButton`

```tsx
<Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
  <Typography variant="h6">Per-Agent Metrics</Typography>
  <Box sx={{ flexGrow: 1 }} />
  <ToggleButtonGroup
    value={metricsDuration}
    exclusive
    onChange={(_, v) => { if (v) setMetricsDuration(v); }}
    size="small"
  >
    <ToggleButton value="1h">1h</ToggleButton>
    <ToggleButton value="6h">6h</ToggleButton>
    <ToggleButton value="12h">12h</ToggleButton>
    <ToggleButton value="1d">1d</ToggleButton>
    <ToggleButton value="7d">7d</ToggleButton>
  </ToggleButtonGroup>
  <Tooltip title="Refresh metrics">
    <IconButton size="small" onClick={() => { fetchMetricsOverview(); fetchAgentMetrics(); if (selectedStatsAgent) fetchAgentSamples(selectedStatsAgent); }}>
      <RefreshIcon fontSize="small" />
    </IconButton>
  </Tooltip>
</Box>
```

## Task 5: Add duration selector and refresh button to read-only statistics tab (tabIndex === 2)

**File:** `frontend/console/src/App.tsx` (around line 2236)

Same pattern as Task 4 — replace the "Per-Agent Metrics" heading with the identical header row.

## Task 6: Build frontend and embed

After all code changes, run:
```bash
cd frontend/console && bun run build && cd ../.. && go build ./...
```

## Acceptance Criteria

- T1+T2: `fetchAgentSamples` passes `from`/`to` query params; default range is 1 hour
- T3: No `setInterval` for metrics polling
- T4+T5: Duration selector (1h/6h/12h/1d/7d) and refresh icon button present in both tabs
- T6: Frontend builds and Go binary compiles
