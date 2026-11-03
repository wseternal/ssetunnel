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

### OpenAPI Validation (`openapi.go`)
Parses and validates OpenAPI v3.0/v3.1 YAML. Extracts operations, rejects external `$ref` and path traversal.

### Package (`package.go`)
Module package metadata with `ValidateExecutable()` — rejects symlinks escaping the package directory.

## Platform Notes
- Unix: IPC via Unix domain sockets at `<datadir>/module.sock`.
- Windows: IPC via named pipes at `\\.\pipe\ssetunnel-module-<basedir>`.
- Supervisor process spawning is platform-specific (`supervisor_unix.go`, `supervisor_windows.go`).

## Rules
- All credential comparison must use constant-time operations.
- OpenAPI validation must run before any install path (CLI or IPC).
- Registry file writes are atomic (write to temp, rename).
