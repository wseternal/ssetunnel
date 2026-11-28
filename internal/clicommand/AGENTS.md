# Client Commands

Client-side command workflows shared by `cmd/cli` and the legacy `cmd/ssetunnel` wrapper. Deliberately separate from `internal/command` so `cmd/server` imports only server-side code.

## Commands

- `RunLogin` — interactive username/password (+TOTP/recovery) login via `clientapp.Login`; warns when the tunnel URL cannot be derived from the console URL.
- `RunAgent` / `RunConnect` — flag parsing + clamping, then `clientapp.RunAgent` / `RunConnect`. Agent installs the SIGHUP placeholder; both accept the deprecated `--base` flag with a warning.
- `RunProbe` — delegates to `internal/probe`.
- `RunModule` — module lifecycle (install/start/stop/restart/status/remove/list) over `agentmod.Registry`. `BinaryName` (default `ssetunnel`, set to `cli` by `cmd/cli`) parameterizes all help text.
- `RunConsole` — reads the live console module surface (`list-agents`, `list-modules`, `schema`, `openapi`, `call`) through `clientapp.ConsoleClient`; `help` prints usage. Flags and positionals may appear in any order; one positional means the module ID and the agent is inferred only when exactly one is connected. Output is table, JSON, or raw body; `call` exits non-zero on a non-2xx answer. A module 401 keeps its own status and does not add the session hint. Status errors show the path without query values. Local body and header errors fail before the console client starts.
- `RunUI` — starts `internal/clientserver` on a loopback address, prints the authenticated URL, opens the browser (best-effort, platform-specific `openBrowser`).
- `BuildRunFn` — service run closures for agent/connect; strips `--service-user` defensively.

## Rules

* All workflows delegate to `internal/clientapp`; no transport or session logic here.
* `openBrowser` runs the child with `Run()` in a goroutine so the child is reaped (no zombie for the UI process lifetime).
* Service commands go through `internal/command.DispatchServiceAction`; this package only supplies the run closures.