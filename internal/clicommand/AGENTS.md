# Client Commands

Client-side command workflows shared by `cmd/cli` and the legacy `cmd/ssetunnel` wrapper. Deliberately separate from `internal/command` so `cmd/server` imports only server-side code.

## Commands

- `RunLogin` — interactive username/password (+TOTP/recovery) login via `clientapp.Login`; warns when the tunnel URL cannot be derived from the console URL.
- `RunAgent` / `RunConnect` — flag parsing + clamping, then `clientapp.RunAgent` / `RunConnect`. Agent installs the SIGHUP placeholder; both accept the deprecated `--base` flag with a warning.
- `RunProbe` — delegates to `internal/probe`.
- `RunModule` — module lifecycle (install/start/stop/restart/status/remove/list) over `agentmod.Registry`. `BinaryName` (default `ssetunnel`, set to `cli` by `cmd/cli`) parameterizes all help text.
- `RunUI` — starts `internal/clientserver` on a loopback address, prints the authenticated URL, opens the browser (best-effort, platform-specific `openBrowser`).
- `BuildRunFn` — service run closures for agent/connect; strips `--service-user` defensively.

## Rules

* All workflows delegate to `internal/clientapp`; no transport or session logic here.
* `openBrowser` runs the child with `Run()` in a goroutine so the child is reaped (no zombie for the UI process lifetime).
* Service commands go through `internal/command.DispatchServiceAction`; this package only supplies the run closures.