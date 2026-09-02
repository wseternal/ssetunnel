# Goal Achieved — Connect Service Commands

## Iterations: 1/10

## Gates Passed
- [x] Connect Service Dispatch
- [x] Named Service Identity
- [x] Build and Test Green

## Commits
- `b9d011b`: feat: add service commands (start/stop/restart/status/uninstall) for connect sub-command

## Working Tree
- Status: clean (after artifact commit)
- Branch: main

## Unresolved Findings (non-blocking)
- Warning: none
- Suggestion: none

## Summary
Added service commands (start, stop, restart, status, uninstall) to the `connect` sub-command with mandatory `--name` argument for multi-instance support. The implementation extends the existing `dispatchServiceAction` infrastructure with:
- `--name` extraction and validation for connect subcommand
- Service name pattern `ssetunnel-connect-<name>` for unique instance identity
- Stdio mode (`--local -`) rejection for daemon services
- Reload (SIGHUP) rejection for connect (no reloadable config)
- Root-guard skip for connect (no embedded postgres dependency)
- 6 unit tests covering all new error paths and helper functions
