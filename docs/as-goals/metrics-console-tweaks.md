# Metrics Console Page Tweaks

## Goal
Tweak the per-agent metrics console page to default to 1-hour view, add a duration range selector, and replace real-time auto-refresh with a manual refresh button.

## Context
- The frontend (`frontend/console/src/App.tsx`) currently polls `fetchMetricsOverview()` and `fetchAgentMetrics()` every 10 seconds via `setInterval`
- The per-agent samples endpoint (`/api/v1/metrics/agents/{agentID}/samples`) already supports `from` and `to` query parameters (default: last 24 hours)
- The frontend calls `fetchAgentSamples(agentID)` without any `from`/`to` params — always gets 24h
- There's already a "Refresh" button in the Statistics page header, but auto-polling also runs

## Success Criteria
- Page defaults to showing only the most recent 1 hour of metric samples
- Duration range selector offers: 1h (default), 6h, 12h, 1d, 7d
- No automatic real-time refresh of metrics data (remove the 10s polling interval for metrics)
- A refresh icon button triggers manual data reload for metrics
- Backend API unchanged (already supports `from`/`to` params)

## Constraints
- Frontend-only change (no backend API modification needed)
- Preserve existing user-made changes in `cmd/ssetunnel/main.go` (service actions for connect)
- Keep the existing Refresh button on the PageHeader functional
- Duration selector and refresh button should be placed near the metrics section header

## Out of Scope
- Changes to the metrics overview section
- Changes to the desktop metrics polling (separate feature)
- Backend API modifications
- Session/agent/connected-agents auto-refresh (keep those as-is)

## Created
2026-09-02T00:00:00Z
