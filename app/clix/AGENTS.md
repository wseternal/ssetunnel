# Clix — Wails Desktop App

Clix is the Wails v3 desktop application for the ssetunnel client surface. It provides native OS service bindings for agent and connect management, replacing the HTTP bridge with direct Wails RPC calls.

## Status

**Skeleton only.** All `ClientService` methods return `"not implemented"`. The Wails bridge in the frontend exports `null`, so the SPA falls through to the HTTP bridge (which has no server in Clix). This is acknowledged remaining work per ADR-005.

## Build Requirements

- **Wails v3:** `go install github.com/wailsapp/wails/v3/...@latest`
- **`go:embed`:** `build/appicon.png` and `build/config.yml` must be tracked in git (`.gitignore` has `!app/clix/build/` exception).
- **Separate module:** Clix has its own `go.mod`; build with `GOWORK=off go build ./app/clix/...`
- **Bindings:** `GOWORK=off wails3 generate bindings` regenerates the Wails bridge types.

## Key Files

| File | Purpose |
|------|---------|
| `main.go` | Wails app definition, `ClientService` stub methods, `OnShutdown` hook |
| `build/appicon.png` | Application icon (embedded via `go:embed`) |
| `build/config.yml` | Wails build configuration |
| `Taskfile.yml` | Build tasks (see root `Taskfile.yml` `clix:*` targets) |

## Excluded from Release

Clix is **not** included in `release.yml` or CI builds. It is committed for development only. The release matrix builds `cmd/ssetunnel` and `cmd/server` only.
