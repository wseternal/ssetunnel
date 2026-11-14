# Command Layer

Server workflow plus shared CLI infrastructure used by all three entry points (`cmd/server`, `cmd/cli`, `cmd/ssetunnel`). Client-side commands live in `internal/clicommand` so the server binary never imports client packages.

## Contents

- `command.go` — `RunServer` (flags, listeners, metrics/auto-tuner, auth store, console wiring), usage texts, deprecated-flag shims (`AcceptDeprecatedBaseFlag`, `WarnDeprecatedBase`), `IsEmbeddedPostgres`.
- `service.go` — OS service management via `kardianos/service`: `ServiceActions`, `DispatchServiceAction` (multi-command binaries), `DispatchServiceActionBare` (single-command binaries), `RunFnBuilder`, argv construction, saved-args persistence, `ExtractFlag`/`StripFlag`/`StripRetiredBase`.
- `version.go`, `sighup_unix.go`, `sighup_windows.go` — build version string and SIGHUP placeholder handler.

## Service Argv Contract

The OS service manager invokes `binary <Arguments...>`.

| Binary | Arguments | Dispatcher |
|--------|-----------|------------|
| `ssetunnel`, `cli` | `["<subcommand>", "run", …flags]` | `DispatchServiceAction` → `BuildServiceArgs` |
| `server` | `["run", …flags]` | `DispatchServiceActionBare` → `BuildRunArgs` |

`BuildServiceArgs = [subcommand] + BuildRunArgs`; the legacy shape is pinned by `service_test.go` and must not change (installed unit files survive upgrades). `cmd/server` also accepts the legacy prefixed form (`server run …`) for in-place binary upgrades. Service names are always `ssetunnel-<subcommand>` (connect: `ssetunnel-connect-<name>`) regardless of which binary installs them, so all binaries manage the same services.

`--service-user` is an installer flag (consumed into `service.Config.UserName`), stripped from runtime argv. `--base` is retired and stripped everywhere, including saved args.

## Rules

* `RunServer` opens listeners eagerly so address-in-use errors surface before serving; console `WriteTimeout` must stay 0 (SSE).
* Never add client-package imports here — `cmd/server`'s dependency boundary depends on it. Known residual: this package imports `consoleserver`, so client binaries transitively embed the console bundle.
* `flag.Parse` stops at the first non-flag token: any argv prefix a binary does not dispatch itself silently drops every flag. Keep the argv contract above in sync with each entry point's dispatch.