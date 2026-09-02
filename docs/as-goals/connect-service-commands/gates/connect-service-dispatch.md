# Gate: Connect Service Dispatch

## Condition
The `connect` sub-command in `main()` dispatches service actions (start, stop, restart, status, uninstall) via `dispatchServiceAction` when the first positional arg is a service verb. The `--name` flag is extracted before dispatch and is mandatory — missing `--name` returns a clear error.

## Evidence Required
- [ ] `cmd/ssetunnel/main.go`: `case "connect":` block includes service action dispatch
- [ ] `cmd/ssetunnel/service.go`: `dispatchServiceAction` handles `connect` subcommand with `--name`
- [ ] `--name` extraction and validation logic
- [ ] Stdio mode (`--local -`) rejected for service actions

## Verification Method
Code review of dispatch path + unit test for `--name` requirement

## Owner
Engineer
