# Agent Module Infrastructure (agentmod)

IPC server, module registry with persistence, process supervisor, and OpenAPI validation for the agent-side module system.

## Components

### IPC Server (`ipc.go`)
Unix domain socket (0600) or Windows named pipe server. Protocol: `AUTH <len>\n<cred>` → `OK\n` → `REG ...` → `OPENAPI <len>\n<yaml>` → `HEALTH ...` loop.

- Credential verification uses `crypto/subtle.ConstantTimeCompare` to prevent timing attacks.
- `ListenerAddr()` accessor is mutex-safe for concurrent `startProcess` calls.
- Platform-specific: `ipc_unix.go` (Unix sockets), `ipc_windows.go` (named pipes via `go-winio`).

### Registry (`registry.go`)
Persistent module registry backed by JSON file in agent data dir. Tracks installed packages, enabled state, and IPC registrations.

- `Install()` validates OpenAPI before persisting.
- `InstallFromIPC()` also validates OpenAPI (modules registering via IPC get the same validation).
- `Supervisor` manages module process lifecycle with exponential backoff restart (500ms → 30s with jitter).

### Module Scan (`scan.go`)
`Registry.ScanModules(dir)` registers packages installed under a directory, one subdirectory per package. Each package holds an executable and `openapi.yaml`.

- The scanner runs `<binary> -print-manifest` to read the module identity. Module IDs come from the binary, never from a manifest file.
- A probed package records the executable's size and mtime (`binary_size`, `binary_mtime` in `packages.json`). A later scan whose stat and spec match reuses that record instead of probing again; any difference re-probes. Comparison is stat-based, so a replacement that preserves both size and mtime is invisible.
- The probe budget defaults to 2 s and is overridable per registry (`SetManifestProbeTimeout`, `--module-probe-timeout` on the agent). Output past 64 KiB cancels the probe immediately; a child holding the output pipe adds up to the 1 s `cmd.WaitDelay` after a kill.
- A package found for the first time is installed and enabled. A registered package keeps its desired state, so an explicit stop survives an agent restart.
- A broken package is logged and skipped, and the other packages still load. `openapi.yaml` must be a regular file, not a symlink.

### OpenAPI Validation (`openapi.go`)
Parses and validates OpenAPI v3.0/v3.1 YAML. Extracts operations, rejects external `$ref` and path traversal.

### Package (`package.go`)
Module package metadata with `ValidateExecutable()` — rejects symlinks escaping the package directory. The executability check is platform-specific (`executable_unix.go`, `executable_windows.go`): the permission bit on Unix, the `.exe` extension on Windows.

## Platform Notes
- Unix: IPC via Unix domain sockets at `<datadir>/module.sock`.
- Windows: IPC via named pipes at `\\.\pipe\ssetunnel-module-<basedir>`.
- Supervisor process spawning is platform-specific (`supervisor_unix.go`, `supervisor_windows.go`).

## Rules
- All credential comparison must use constant-time operations.
- OpenAPI validation must run before any install path (CLI or IPC).
- Registry file writes are atomic (write to temp, rename).
