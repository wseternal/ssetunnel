# Agent Modules Guide

## Overview

Agent modules extend the SSE tunnel with pluggable HTTP services that run as separate processes alongside the agent. Each module exposes an OpenAPI-defined HTTP API that consumers can invoke through the tunnel's secure transport.

## Architecture

```
Consumer (SDK/HTTP)
    │
    ▼
┌─────────────────────────────────────────────────────────┐
│  Server                                                  │
│  • ModuleCatalog: validates and stores agent catalogs    │
│  • ModuleStreamHandler: correlates invocation streams    │
│  • Console API: /api/v1/modules/...             │
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

## Concepts

### Module Package

A module package is a directory containing:
- `module.json` — manifest with ID, version, executable path
- `openapi.yaml` — OpenAPI v3.0 or v3.1 specification
- Executable binary
- Optional UI assets for federation

```json
{
  "module_id": "com.example.weather",
  "service_name": "Weather Service",
  "service_version": "1.2.0",
  "revision": "abc123",
  "executable": "./weather-server"
}
```

### Registry and Desired State

The registry tracks installed packages and their desired lifecycle state (enabled/disabled). State persists to `~/.ssetunnel/agent/desired_state.json` so modules restart after agent process restarts.

```bash
# Install a module
ssetunnel module install --package ./weather-module/

# Enable (starts the process)
ssetunnel module start weather

# Disable (stops the process)  
ssetunnel module stop weather

# Uninstall
ssetunnel module remove weather
```

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

### Header Sanitization

Before forwarding to modules, the server strips:
- Authorization, Cookie, X-SSET-Token headers
- Hop-by-hop headers (Connection, Transfer-Encoding, etc.)

## Provider SDK

Module authors implement a standard `http.Handler`:

```go
package main

import (
    "net/http"
    "github.com/wseternal/ssetunnel/sdk"
)

func main() {
    manifest := &sdk.Manifest{
        ModuleID:       "com.example.weather",
        ServiceName:    "Weather Service",
        ServiceVersion: "1.2.0",
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
# Install and enable modules
ssetunnel module install --package ./modules/weather/
ssetunnel module start com.example.weather

# Agent will publish catalog on next connect
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
