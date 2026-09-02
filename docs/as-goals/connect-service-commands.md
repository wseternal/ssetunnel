# Connect Service Commands

## Goal
Support service commands (start, stop, restart, status, uninstall) for the `connect` sub-command with a mandatory `--name` argument, so users can run multiple named connect daemons simultaneously.

## Context
The `agent` and `server` sub-commands already support OS service management (start, stop, restart, status, reload, uninstall) via `dispatchServiceAction()` in `cmd/ssetunnel/service.go`. Each subcommand registers a single service named `ssetunnel-<subcommand>`. The `connect` sub-command currently runs only in foreground mode. Users need to run multiple connect daemons (e.g., one for SSH to agent-A, another for DB to agent-B), each as a named OS service.

## Success Criteria
- `ssetunnel connect start --name <n> --agent <a> --target <t> --local <l>` installs and starts a named OS service
- `ssetunnel connect stop --name <n>` stops the named service
- `ssetunnel connect restart --name <n>` restarts the named service
- `ssetunnel connect status --name <n>` reports the named service status
- `ssetunnel connect uninstall --name <n>` stops, uninstalls, and cleans up the named service
- `--name` is mandatory for all connect service actions, optional/ignored for bare connect
- Bare `ssetunnel connect --local ...` (no service action) still works as before
- Multiple named connect services can run simultaneously without conflict
- Stdio mode (`--local -`) is rejected for service actions

## Constraints
- Reuse existing `dispatchServiceAction` infrastructure as much as possible
- Service name pattern: `ssetunnel-connect-<name>` to differentiate instances
- No `reload` (SIGHUP) support for connect services
- `--name` flag is consumed by the service layer, not passed to `runConnect`

## Out of Scope
- `reload` (SIGHUP) support for connect services
- Changes to the connect client library (`internal/connect/`)
- Frontend/console changes
- Changes to existing agent/server service commands

## Created
2026-09-02
