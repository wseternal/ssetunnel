# Module SDK (sdk)

Public Go SDK for module authors (Provider) and consumers (Consumer/Discovery).

## Components

### Provider (`provider.go`)
Module authors embed the Provider to register with the agent's IPC server, report health, and serve HTTP invocations.

- `Serve(ctx, manifest, handler)` — connects to IPC, registers, sends OpenAPI, and serves invocations.
- Auto-reconnect with two-phase backoff: 2s × 5 fast phase, then exponential 1s → 30s cap.
- `IPCClient` handles AUTH → REG → OPENAPI → HEALTH protocol.
- Platform-specific network: `ipc_unix.go` (`"unix"`), `ipc_windows.go` (`"unix"` via go-winio).

### Consumer (`discovery.go`)
Module consumers discover and invoke modules through the tunnel server.

- `Discover(ctx, agentID)` — fetches the module catalog (includes epoch via `X-Catalog-Epoch` header).
- `Contract(ctx, ref)` — fetches full OpenAPI YAML for a specific module revision.
- `Invoke(ctx, ref, req)` — sends an HTTP request through the tunnel to a module.

### Manifest (`manifest.go`)
Module identity and validation. Reverse-DNS module IDs, OpenAPI YAML, UI descriptors.

- `PrintManifest(m)` prints the module identity as JSON when the process was started with `-print-manifest`, and reports whether it printed. The agent uses it to register an installed package before it starts the module.
- Module main functions call `PrintManifest` before flag parsing and before any other stdout output. The agent reads the identity from stdout.

### Errors (`errors.go`)
Structured error types with HTTP status codes for consumer-facing error handling.

## Rules
- Provider reconnection is automatic — module authors should not manage connection state.
- Consumers must use the catalog epoch from `Discover` for subsequent invocations (stale epochs → 409).
- SDK constants are intentionally duplicated from `internal/moduleproto` (public API isolation from internal types).
