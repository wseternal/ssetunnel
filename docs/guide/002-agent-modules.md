# Agent Modules Guide

## Overview

Agent modules extend the SSE tunnel with pluggable HTTP services that run as separate processes alongside the agent. Each module exposes an OpenAPI-defined HTTP API that consumers can invoke through the tunnel's secure transport.

For the operational walkthrough — build and install the builtin packages, load them, verify, invoke, update, and troubleshoot — see [004-builtin-modules-quickstart.md](004-builtin-modules-quickstart.md).

## Architecture

```
Consumer (SDK/HTTP)
    │
    ▼
┌─────────────────────────────────────────────────────────┐
│  Server                                                  │
│  • ModuleCatalog: validates and stores agent catalogs    │
│  • ModuleStreamHandler: correlates invocation streams    │
│  • Console API: /api/v1/modules/...                      │
└──────────┬──────────────────────────────────────────────┘
           │  Control stream: snapshot sync (start/chunk/commit)
           │  Data streams: reverse-opened HTTP invocations
           ▼
┌─────────────────────────────────────────────────────────┐
│  Agent                                                   │
│  • ModuleManager: publishes catalog snapshots            │
│  • ModuleInvoker: handles module_open, proxies HTTP      │
│  • Registry: tracks installed packages, desired state    │
│  • Supervisor: manages module process lifecycle          │
│  • IPCServer: owner-only Unix socket / named pipe        │
└──────────┬──────────────────────────────────────────────┘
           │  Local IPC (Unix socket / Windows named pipe)
           ▼
       Module Process (provides http.Handler)
```

## Surfaces

A running agent exposes its modules through three surfaces with identical semantics. The HTTP policy behind all of them lives in `internal/moduleapi`; see [ADR-007](../adr/007-module-surface-parity.md).

| Surface | Reach | Auth | Path prefix |
|---------|-------|------|-------------|
| Console | public server → tunnel → agent | user session | `/console/api/v1/modules/...` |
| `cli ui` | browser → local client server → agent | launch secret | `/api/v1/modules/...` |
| Local script | agent loopback listener | none (loopback boundary) | `http://127.0.0.1:<port>/api/v1/modules/...` |

The agent publishes its loopback base URL in the status file `~/.ssetunnel/ssetunnel-agent.json` as `module_api_url`, and logs it at startup (`agent: module API listening on http://127.0.0.1:<port>`). `cli ui` reads that field, re-validates it on every proxied request, and hides the Modules page when it is absent.

```bash
# What the agent published
jq -r .module_api_url ~/.ssetunnel/ssetunnel-agent.json

# Discovery, metadata, OpenAPI, operation models — no credentials
curl -s "$BASE/api/v1/modules?agent=local" | jq
curl -s "$BASE/api/v1/modules/local/echo" | jq
curl -s "$BASE/api/v1/modules/openapi/local/echo"
curl -s "$BASE/api/v1/modules/local/echo/operations" | jq

# Invocation: both route forms, query strings and bodies preserved
curl -s "$BASE/api/v1/modules/invoke/local/echo/say?text=hello"
curl -s -X POST "$BASE/api/v1/modules/invoke/local/echo/say" \
  -H 'Content-Type: application/json' --data '{"text":"hello"}'
```

Boundary rules a caller must satisfy (see `localBoundary`): loopback peer, `Host` equal to the listener address, `Origin` (when present) exactly `http://<listener>`, `Sec-Fetch-Site` `same-origin` or `none`, no CORS preflight. A configured `AgentID` is required; when none is configured, use `local`.

`cli ui` requests never traverse the console or the tunnel: the browser calls the local client server, which proxies to the loopback listener. The cURL snippets the Modules page shows point at `module_api_url` — the endpoint a script on the same machine can call directly.

## Concepts

### Module Package

A module package is a directory containing:
- Executable binary — answers `-print-manifest` with its identity, as a regular file (a symlink is refused). On Windows the file must end in `.exe`, because Windows has no executable bit to check
- `openapi.yaml` — OpenAPI v3.0 or v3.1 specification, as a regular file (a symlink is refused)
- Optional UI assets for federation

Builtin modules live under `cmd/modules/<name>/` in the repository. `task modules:install` builds each one and installs it to `~/.ssetunnel/modules/<name>/`:

```
~/.ssetunnel/modules/ai-automation-proxy/
├── ai-automation-proxy     # binary, named after the directory
└── openapi.yaml
```

At startup the agent scans `~/.ssetunnel/modules`. For each subdirectory it reads `openapi.yaml` and stats the binary. A package whose binary size, modification time, and spec are unchanged since it was registered keeps its recorded identity and is not executed again. A new or changed package is run with `-print-manifest` and then registered. On first registration the package is enabled; an already-registered module keeps its desired state.

Placing a binary and its `openapi.yaml` in `~/.ssetunnel/modules/<name>/` is equivalent to installing the module. The agent picks it up on the next scan. An agent reload (SIGHUP) also rescans the directory, so a new package loads without a full restart. `task modules:install` replaces the binary, which changes its modification time, so the next scan probes it again. The comparison uses size and modification time only; a binary replaced in place with both preserved would be missed.

Each probe gets 2 seconds. A module that needs longer on a cold start (Gatekeeper assessment, cold page cache) is skipped with a log line and retried on the next scan; raise the budget with `cli agent --module-probe-timeout 5s`. A probe that writes more than 64 KiB is rejected and cancelled without waiting out the budget.

### Registry and Desired State

The registry tracks installed packages and their desired lifecycle state (enabled/disabled). State persists to `~/.ssetunnel/agent/desired_state.json` so modules restart after agent process restarts.

```bash
# Build and install all builtin modules into ~/.ssetunnel/modules
task modules:install

# Enable (starts the process)
ssetunnel module start com.example.weather

# Disable (stops the process)
ssetunnel module stop com.example.weather

# Inspect installed modules
ssetunnel module list
ssetunnel module status com.example.weather

# Uninstall: unregister first, then delete the files
ssetunnel module stop com.example.weather     # a running module must stop first
ssetunnel module remove com.example.weather   # drops the registry entry
rm -rf ~/.ssetunnel/modules/weather           # deletes the package directory
```

`module remove` unregisters the module. Deleting the directory alone leaves a registry entry that the agent keeps trying to start, so always remove the entry first.

### Catalog Snapshots

When a module is enabled or the registry changes, the agent publishes a catalog snapshot to the server:

1. **SnapshotStart** — declares epoch, module count, total size
2. **SnapshotChunk** — 32 KiB chunks of module entries
3. **SnapshotCommit** — SHA-256 digest for verification

The server validates OpenAPI documents before committing the catalog.

### Invocation Flow

For each invocation:

1. Consumer calls `Invoke(agentID, moduleID, request)`
2. Server sends `module_open` control message to agent
3. Agent validates module exists and revision matches
4. Agent opens reverse yamux stream with `__module__\n<invocationID>` marker
5. Server correlates stream with pending invocation
6. HTTP exchange flows: Consumer → Server → Agent → Module Process → Agent → Server → Consumer

## Security Model

### Process Isolation

Each module runs as a separate process. The supervisor manages lifecycle with:
- Direct exec (no shell)
- Process group isolation (`Setpgid`)
- Jittered restart backoff (500ms → 30s cap)
- Graceful shutdown (SIGTERM + 5s grace, then SIGKILL)

### IPC Security

Modules communicate with the agent over:
- **Unix**: Owner-only socket (mode 0600) at `{dataDir}/module.sock`
- **Windows**: Named pipe with security descriptor allowing only the current user

Each module process receives a random 32-byte credential via environment variable for authentication.

### Authorization

Invocations require:
- Valid user session (console) or bearer token (SDK)
- Connect permission
- User-scoped agent access
- OpenAPI operation matching (method + path)
- Epoch and revision pinning (stale references rejected with 409)

The agent's loopback surface skips user authentication by design; its access control is the loopback/Host/Origin/fetch-metadata boundary plus the same operation matching, revision pinning, body limits, timeouts, and invocation caps. Any other OS user on the same machine can reach a loopback listener — do not enable modules on machines where that is unacceptable.

### Header Sanitization

Before forwarding to modules, the server strips:
- Authorization, Cookie, X-SSET-Token headers
- Hop-by-hop headers (Connection, Transfer-Encoding, etc.)

## Provider SDK

Module authors implement a standard `http.Handler`:

```go
package main

import (
    "context"
    _ "embed"
    "log"
    "net/http"

    "github.com/wseternal/ssetunnel/sdk"
)

//go:embed openapi.yaml
var openapiYAML []byte

func main() {
    manifest := sdk.Manifest{
        ModuleID:       "com.example.weather",
        ServiceName:    "weather",
        ServiceVersion: "1.2.0",
        OpenAPIYAML:    openapiYAML,
    }

    // Answer the agent's manifest probe, then serve.
    if sdk.PrintManifest(manifest) {
        return
    }

    handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Handle requests per OpenAPI spec
        w.Write([]byte(`{"temperature": 72}`))
    })

    if err := sdk.Serve(context.Background(), manifest, handler); err != nil {
        log.Fatal(err)
    }
}
```

`PrintManifest` must run before flag parsing and before any other stdout output. The agent reads the identity from stdout.

The SDK handles IPC connection, registration, health reporting, and asset serving.

## Consumer SDK

Consumers discover and invoke modules:

```go
consumer := sdk.NewConsumer("https://tunnel.example.com", authToken, nil)

// Discover available modules
catalog, err := consumer.Discover(ctx, "my-agent")

// Fetch OpenAPI spec
contract, err := consumer.Contract(ctx, sdk.ModuleRef{
    AgentID:  "my-agent",
    ModuleID: "com.example.weather",
    Revision: "abc123",
})

// Invoke an operation
req, _ := http.NewRequest("GET", "/weather?city=SF", nil)
resp, err := consumer.Invoke(ctx, ref, req)
defer resp.Body.Close()
```

## Limits

| Limit | Value |
|-------|-------|
| Modules per agent | 64 |
| OpenAPI YAML size | 1 MiB |
| Catalog total size | 4 MiB |
| Snapshot chunk size | 32 KiB |
| Pending opens | 32 |
| Active invocations | 32 |
| Per-module invocations | 8 |
| Establishment deadline | 3 seconds |
| Request headers | 64 KiB |
| Request path | 8 KiB |
| Request query | 16 KiB |
| UI asset size | 8 MiB |
| UI cache size | 64 MiB |

## Troubleshooting

### Module not appearing in catalog

1. Check module is enabled: `ssetunnel module status <id>`
2. Check process is running: `ps aux | grep <module>`
3. Check agent logs for snapshot errors
4. Verify OpenAPI YAML is valid

### Invocation timeout

1. Check module process health
2. Verify network connectivity between agent and server
3. Check pending opens limit (32 max)
4. Review server logs for `establishment timeout`

### Stale revision errors (409)

The consumer's cached revision doesn't match the agent's current revision. Call `Discover()` again to get the latest catalog.

## Mixed-Version Compatibility

- Old agents (no modules capability) ignore `modules=1` in capability negotiation
- Old servers ignore snapshot messages from new agents
- Invocations only work when both sides negotiate `modules=1`
- Existing TCP, shell, and desktop streams are unaffected

## Deployment

### Agent Configuration

```bash
# Install builtin modules into ~/.ssetunnel/modules
task modules:install

# Start the agent. It scans the modules directory and enables
# every package it finds.
ssetunnel agent --server https://tunnel.example.com --agent mybox
```

### Server Configuration

No additional configuration needed. Module routes are automatically available when the console is enabled.

### Monitoring

Module metrics are exposed via the metrics API:
- `catalog_size` — current module count
- `invocations_total` — total invocation attempts
- `invocations_failed` — failed invocations
- `errors_transport` — transport-layer errors
- `bytes_sent/received` — data plane throughput
