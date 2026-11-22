# Builtin Modules — Quick-Start Guide

This guide takes you from a clean checkout to a running builtin module: build and install the packages, let the agent discover them, verify what loaded, then call one. It also covers updating a package, adding your own to the builtin set, and the log lines that explain a package the agent refused.

Builtin modules are ordinary agent modules that ship in this repository under `cmd/modules/<name>/`. The build task installs each one as a package directory under `~/.ssetunnel/modules/`, and the agent scans that directory at startup. No configuration, no catalog registration, no per-module flags.

The first builtin module is `ai-automation-proxy` (`com.wseternal.ai-automation-proxy`), which accepts automation jobs over HTTP and hands them to the `gemini-automation-extension` browser extension. Its own testing workflow lives in [003-testing-ai-automation-proxy.md](003-testing-ai-automation-proxy.md).

## Prerequisites

- Go 1.26 or later
- `task` (Taskfile) — the module build runs through it
- An agent that will run on this machine, with a server URL and token

## Step 1: Build and install the builtin modules

```bash
task modules:install
```

The task walks every directory under `cmd/modules/`, requires an `openapi.yaml` next to each `main` package, and writes one installed package per module:

```bash
$ task modules:install
installed /Users/you/.ssetunnel/modules/ai-automation-proxy/ai-automation-proxy
installed 1 module(s) into /Users/you/.ssetunnel/modules — restart the agent to load them
```

The installed layout puts the binary and its spec in one directory named after the module:

```
~/.ssetunnel/modules/
└── ai-automation-proxy/
    ├── ai-automation-proxy     # the binary, named after the directory
    └── openapi.yaml            # copied verbatim from cmd/modules/ai-automation-proxy/
```

Two details matter here:

- **The directory name is the binary name.** The scan looks for `<dir>/<dir>` first and only falls back to another regular file when exactly one candidate remains, so a mismatched name usually still loads but breaks as soon as the directory holds a second file.
- **The spec stays a separate file.** The binary also embeds its own copy for the manifest probe; the on-disk `openapi.yaml` is what the server validates and what the console renders.

If you have an install from before this layout existed, `~/.ssetunnel/modules/` holds the binary and `openapi.yaml` as loose files at the top level. The scan only looks at immediate subdirectories, so those loose files are ignored. Delete them and re-run the task.

## Step 2: Load the packages

Start the agent:

```bash
ssetunnel agent --server https://tunnel.example.com --id mybox
```

At startup the agent scans `~/.ssetunnel/modules` before it reconciles the supervisor, and the log shows what it found and what it started:

```
agent: module IPC listening on /Users/you/.ssetunnel/agent/module.sock
agent: loaded 1 module package(s) from /Users/you/.ssetunnel/modules
module[com.wseternal.ai-automation-proxy]: aiproxy: starting module com.wseternal.ai-automation-proxy (extension server on 127.0.0.1:9090)
```

A module's output has two paths into that log: stderr keeps the plain `module[<module-id>]:` prefix, and stdout is tagged `module[<module-id>] out:`. Both are forwarded line by line, so a module can log to whichever stream it prefers and the agent's log stays the single place its output lives. Lines from the two streams can interleave; each stream's own order is preserved.

For each package directory the scan:

1. Requires `openapi.yaml` and the executable to be regular files — a symlink in either place is refused before anything runs.
2. Reuses the registered entry when the executable's size and modification time and the spec bytes are unchanged, so a restart does not re-run the module.
3. Otherwise runs the binary with `-print-manifest` for up to 2 seconds and reads the identity it prints.
4. Installs the package, and **enables a package it has not seen before**, so a first install starts without a second command.

The scan failure lines name the package directory, and a broken package never blocks the others:

```
registry: skip module package /Users/you/.ssetunnel/modules/x: read /Users/you/.ssetunnel/modules/x/openapi.yaml: open ...: no such file or directory
registry: skip module package /Users/you/.ssetunnel/modules/y: /Users/you/.ssetunnel/modules/y/y is a symlink (not allowed)
```

If the agent is already running as a service, a reload rescans without a restart:

```bash
ssetunnel agent reload      # SIGHUP → registry reload → scan → reconcile
```

Reload is a SIGHUP, so it applies to the Unix service; on Windows restart the agent instead.

The CLI module commands also send SIGHUP for you, so a `module start`/`stop`/`remove` takes effect on the running agent immediately (best-effort: it is silent when no agent is running).

## Step 3: Verify what loaded

`module list` reads the registry from disk, so it works even when the agent is down:

```bash
$ ssetunnel module list
MODULE ID                            SERVICE                VERSION  STATUS
com.wseternal.ai-automation-proxy    ai-automation-proxy    0.1.0    enabled
```

`module status` adds the exact executable the supervisor will spawn:

```bash
$ ssetunnel module status com.wseternal.ai-automation-proxy
Module:     com.wseternal.ai-automation-proxy
Service:    ai-automation-proxy v0.1.0
Executable: /Users/you/.ssetunnel/modules/ai-automation-proxy/ai-automation-proxy
Status:     enabled
```

From the console: open the admin console, go to the **Modules** tab, pick the agent from the selector, and the module card appears with its OpenAPI operations. An empty list there while `module list` shows the module means the agent is not connected or the catalog has not synced yet.

Registry state lives next to the socket, in the agent data directory:

| File | Purpose |
|------|---------|
| `~/.ssetunnel/agent/packages.json` | installed packages, including binary size and mtime |
| `~/.ssetunnel/agent/desired_state.json` | enabled/disabled per module |
| `~/.ssetunnel/agent/module.sock` | owner-only IPC socket the modules connect back on |

## Step 4: Use a module

Invocations travel through the server, so a module never opens a port that you dial directly — the kernel-side listeners belong to the agent. (The `ai-automation-proxy` module additionally binds a local port for the browser extension, default `:9090`; that is the extension's own channel, not the invocation path.)

Given a module card in the console, the **Module API Workbench** builds its request form from the operations in `openapi.yaml`, so there is nothing module-specific to install:

1. Select the agent and module in the **Modules** tab
2. Pick an operation from the list on the left — path is the template from the spec
3. Fill the path/query fields (path parameters are always required) and the JSON body, which is pre-filled from the spec's schema (examples, defaults, and enum values are used when present)
4. **Send**, or copy the generated cURL and replay it from a shell

Responses show status, timing, headers, and a pretty-printed body; the pane streams the body and stops at 200 KiB, so a large artifact is never held in the tab in full. Query fields left blank are omitted, and the JSON body is validated locally before the request leaves the console. If an operation's schema is nested or recursive, the template stops after a few levels and the body stays freely editable. A body the spec declares as non-JSON (for example `multipart/form-data`) opens empty with only its `Content-Type` header set, because a JSON skeleton would misdescribe the call.

The same request as an HTTP call against the console API. The console port is 8081 by default; behind a reverse proxy add whatever path prefix the proxy expects:

```bash
curl -sS -X POST \
  -H "Authorization: Bearer $SSETUNNEL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"list the open tabs"}' \
  http://localhost:8081/api/v1/modules/invoke/mybox/com.wseternal.ai-automation-proxy/jobs
```

The job is accepted with `202` and an id; poll `GET /jobs/{job_id}` for the result. The job endpoints, schemas, and status codes are in `cmd/modules/ai-automation-proxy/openapi.yaml` — that file is the contract, the console renders it, and the server rejects operations that are not in it.

From Go, the consumer SDK does discovery and invocation without hand-built URLs. Point it at the console URL — port 8081 locally, or the console path on a reverse proxy — and carry the catalog's epoch and revision in the reference:

```go
consumer := sdk.NewConsumer("http://localhost:8081", token, nil)

ctx := context.Background()
catalog, err := consumer.Discover(ctx, "mybox")

var ref sdk.ModuleRef
for _, m := range catalog.Modules {
    if m.ModuleID == "com.wseternal.ai-automation-proxy" {
        ref = sdk.ModuleRef{
            AgentID:  "mybox",
            ModuleID: m.ModuleID,
            Epoch:    catalog.Epoch,
            Revision: m.Revision,
        }
    }
}

req, _ := http.NewRequest("POST", "/jobs", strings.NewReader(`{"prompt":"list the open tabs"}`))
req.Header.Set("Content-Type", "application/json")
resp, err := consumer.Invoke(ctx, ref, req)
defer resp.Body.Close()
```

Both the agent and the server must negotiate module support (`modules=1`); with an older server the invocation endpoints are absent, and with an older agent the catalog stays empty. Nothing else on the tunnel (TCP, shell, desktop) is affected.

## Step 5: Manage the module lifecycle

```bash
ssetunnel module stop com.wseternal.ai-automation-proxy     # disable: supervisor stops the process
ssetunnel module start com.wseternal.ai-automation-proxy    # enable: supervisor starts it
ssetunnel module restart com.wseternal.ai-automation-proxy  # stop then start
```

Desired state is persisted, so it wins over the scan: a package registered by the scan is enabled once, and after that an explicit `stop` survives agent restarts as well as rescans.

Uninstall takes two steps, in this order:

```bash
ssetunnel module stop com.wseternal.ai-automation-proxy     # a running module must stop first
ssetunnel module remove com.wseternal.ai-automation-proxy   # drop the registry entry
rm -rf ~/.ssetunnel/modules/ai-automation-proxy             # delete the package directory
```

`module remove` only touches the registry. Deleting the directory alone leaves an entry the supervisor keeps trying to start, so remove the entry first.

## Step 6: Update a module

Re-run the install task after changing the module source:

```bash
task modules:install        # rewrites the binary and the spec
ssetunnel module restart com.wseternal.ai-automation-proxy
```

A rebuild replaces the binary, so its size or modification time changes and the next scan re-probes it: the registry takes the identity the new binary prints, and the catalog is republished because the scan changed something. The package keeps its desired state — an enabled module stays enabled, a stopped one stays stopped.

Reloading alone is not enough to switch a running module to the new binary. Reconcile only starts modules that are not running and stops modules that are no longer enabled; a module already running keeps its old process image. `module restart` disables and re-enables it, which stops the old process and starts the new one. A `task modules:install` followed by `ssetunnel agent reload` updates the registry and catalog even while the old process keeps serving, which can look like "the update did nothing".

The skip rule compares size and modification time only. A binary replaced in place with both preserved (`cp -p`, `touch -r`) is not detected, and neither is a rebuild that lands in the same timestamp tick; run `task modules:install` (or `touch` the binary) when in doubt.

If a module needs longer than 2 seconds to answer the probe on a cold start — first launch under Gatekeeper, cold page cache — raise the budget:

```bash
ssetunnel agent --server https://tunnel.example.com --id mybox --module-probe-timeout 5s
```

The flag applies to every package the scan probes; a package that misses it is skipped for that scan and retried on the next reload.

## Step 7: Add your own module to the builtin set

A builtin module is a directory under `cmd/modules/` that follows the packaging rules in [002-agent-modules.md](002-agent-modules.md) and the authoring walkthrough in [002-echo-module-quickstart.md](002-echo-module-quickstart.md):

```
cmd/modules/weather/
├── main.go          # answers -print-manifest, then serves
└── openapi.yaml     # OpenAPI 3.0 or 3.1, operations the server will allow
```

The two entry points a package needs are the probe and the server:

```go
m := sdk.Manifest{
    ModuleID:       "com.example.weather",
    ServiceName:    "weather",
    ServiceVersion: "1.2.0",
    OpenAPIYAML:    openapiYAML, // //go:embed openapi.yaml
}

// Answer the probe before anything else touches stdout, then serve.
if sdk.PrintManifest(m) {
    return
}
if err := sdk.Serve(ctx, m, handler); err != nil {
    log.Fatal(err)
}
```

`PrintManifest` must run before flag parsing and before any other stdout write, because the agent reads the identity from that single JSON line. Once the directory exists, `task modules:install` builds it, copies the spec, and the scan picks the package up like any other — the directory name and the binary name must match.

A module that lives outside `cmd/modules` does not need to be a builtin. Put its binary and `openapi.yaml` into `~/.ssetunnel/modules/<name>/` and the scan treats it the same way. To register a package that must stay where it is, point the CLI at it (the package directory becomes the executable's parent directory, and the module starts out disabled):

```bash
ssetunnel module install com.example.weather weather 1.2.0 \
  --executable /opt/weather/weather \
  --openapi /opt/weather/openapi.yaml
ssetunnel module start com.example.weather
```

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `No modules installed.` after `task modules:install` | Files sit flat in `~/.ssetunnel/modules`, or the agent has not been started since | Re-run the task so each package gets its own directory; start or reload the agent |
| `skip module package ...: no executable found in ...` | Directory has the spec but no binary, or the binary is hidden/irregular | Put `<dir-name>` (or `<dir-name>.exe` on Windows) beside `openapi.yaml` |
| `skip module package ...: ambiguous executables in ...` | More than one non-spec file in the directory | Name the binary after the directory and remove the stray file |
| `skip module package ...: ... is a symlink (not allowed)` | Package installed through a link | Replace the link with the real file, or copy the target into the package |
| `skip module package ...: probe ...: context deadline exceeded` | Probe missed the 2 s budget | `--module-probe-timeout 5s`; if it still misses, the module likely serves instead of answering `-print-manifest` |
| `skip module package ...: manifest output exceeds 65536 bytes` | Binary writes to stdout before or during the probe | Move `sdk.PrintManifest` ahead of all logging; the probe budget is cancelled as soon as the cap is hit |
| `skip module package ...: parse manifest from ...` | Probe printed something other than one JSON identity line | Use `sdk.PrintManifest` alone and keep other output off stdout |
| Module listed but missing in the console | Agent disconnected, or the other side is older than module support | Check the agent log for sync errors; both sides must negotiate `modules=1` |
| Directory contents look correct but nothing changes on reload | Size and mtime both unchanged | `touch` the binary or re-run `task modules:install` |
| Windows: package is skipped although the binary runs | Windows decides by extension, not by mode | The executable must end in `.exe` |

## Reference

- [002-agent-modules.md](002-agent-modules.md) — module architecture, catalog snapshots, invocation flow, security model, and the hard limits (64 modules per agent, 1 MiB spec, 4 MiB catalog)
- [002-echo-module-quickstart.md](002-echo-module-quickstart.md) — writing a module from scratch
- [003-testing-ai-automation-proxy.md](003-testing-ai-automation-proxy.md) — exercising the builtin proxy end to end
- `internal/agentmod/` — the scan, registry, probe, and supervisor internals
