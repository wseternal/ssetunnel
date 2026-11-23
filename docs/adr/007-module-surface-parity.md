# ADR-007: Module Surface Parity — Direct Agent API and Shared Module UI

## Status

Accepted. Implements the module-surface parity goal; supersedes none.

## Context

Modules could only be reached through the console: `GET /api/v1/modules` over the
tunnel, invocation on a reverse-opened yamux data stream, UI assets rendered by
console-only components. Three problems followed from that:

1. **`cli ui` had no module capability.** Its browser client could list an agent,
   start and stop it, and open a cloud shell — but a module nobody could invoke
   from the machine it runs on was effectively console-only.
2. **The console was the only authenticated hop.** Operators on the agent host had
   to log in, reach a public server, and come back down the tunnel to call a
   service listening on `127.0.0.1` in their own process.
3. **Each surface re-derived the same HTTP policy.** Query strings were dropped on
   one path, `OPTIONS` was answered as a CORS preflight even when a module declared
   it, browsers could not send TRACE, provider assets were served without
   checking the descriptor's size/digest, and the module workbench existed as two
   divergent copies.

## Decision

### 1. The agent publishes a loopback module API

`internal/agent/module_api.go` binds `127.0.0.1:0` after `InitModules` reconciles
the supervisor. It serves the same route shapes as the console — discovery,
metadata, OpenAPI, operation models, invocation (both route forms), UI descriptors,
provider and consumer assets — and creates **no** yamux stream: `InvokeLocal` goes
through the same admission, revision check, and IPC dial as the tunnel path.

The boundary is transport-level, not credential-based:

| Control | Rule |
|---------|------|
| Peer | loopback IP only |
| Host | must equal the listener address (DNS-rebinding guard) |
| Origin | if present, exactly `http://<listener>` |
| Sec-Fetch-Site | `same-origin` or `none` only |
| Preflight | refused — this surface does not opt into CORS |
| Agent identity | configured `AgentID` required; when none is configured, `local` |

The catalog epoch is process-scoped (`local-<hex>`), never the tunnel session ID.

**Accepted residual risk:** any other OS user on the same machine can reach a
loopback listener. An IPC design (owner-only Unix socket) would exclude them but
would also exclude every browser surface, which is the point of the goal. The
mitigations are the Host/Origin/fetch-metadata checks above (they stop a remote
page from driving the API through the user's browser) plus module-level operation
matching, timeouts, and invocation caps. Deployments that cannot
accept cross-user loopback access must not enable modules.

### 2. One HTTP policy for all surfaces

`internal/moduleapi` owns the transformation:

- `ResolveMethod` — `POST` + `X-SSET-Method-Override: TRACE` becomes TRACE; nothing
  else may be overridden.
- `IsPreflight` — `OPTIONS` counts as a preflight only with
  `Access-Control-Request-Method`; a bare OPTIONS reaches the module.
- `TrailAfterSegments` — operation paths come from the **escaped** URL, so a path
  parameter containing `%2F` survives; rebuilt route variables would not.
- `BuildRequest` — preserves `RawQuery` and `ContentLength`, strips credentials,
  hop-by-hop, forwarding, browser-metadata, override, and `Accept-Encoding`.
- `CopyResponseHeaders` — allowlist without `cache-control`; `x-catalog-epoch`
  included so a UI can name the snapshot that answered.
- `assets.go` — declared-asset lookup plus size and SHA-256 verification; symlinks
  and escaping paths are refused.

Every surface calls these; none re-derives them.

### 3. `cli ui` proxies instead of reimplementing

The client server registers `/api/v1/modules/**` as a proxy
(`internal/clientserver/module_proxy.go`). Each request is resolved against
`clientapp.ReadExternalAgentStatus().ModuleAPIURL` — the agent writes that into its
status file from `Agent.ModuleAPIURL()` — and validated per request (scheme `http`,
no userinfo, loopback host, explicit port, no path/query/fragment). The browser
keeps its launch secret; the proxy talks to the agent; the agent talks to the
module over IPC. `statusWriter` gained `Flush`/`Unwrap` so responses stream at a
32 KiB cadence.

### 4. One module UI, two adapters

`frontend/shared/modules` holds `ModulesPage` and `ModuleWorkbench`, plain MUI
(no `orca-ui`, which the client does not depend on). Each SPA supplies only a
`ModuleFetch` and the cURL base:

| Surface | Fetch | cURL target |
|---------|-------|-------------|
| Console | `<base>/api/v1/modules/...` + session bearer | console invoke URL, `Bearer $SSETUNNEL_TOKEN` |
| `cli ui` | local proxy `/api/v1/modules/...` + launch secret | the agent's `module_api_url`, no secret |

Because `../shared` lives outside each project root, both vite configs alias
`react`, `react-dom`, `@mui/material`, and `@mui/icons-material` to the project's
own `node_modules` (a symlinked directory would resolve a second React copy).

## Alternatives Considered

- **Owner-only IPC for the direct API.** Strongest isolation, but no browser can
  call it, so `cli ui` would still need a proxy and the "direct" surface would
  serve only CLI clients. Rejected: it does not achieve parity, and it adds a
  second protocol to maintain.
- **Credentials on the loopback API.** A per-agent token would protect against
  cross-user access but would also have to be distributed into `cli ui`, the cURL
  snippets, and every script — re-creating the console's auth friction for a
  same-machine service. Rejected for now; the Host/Origin boundary plus
  invocation limits is the accepted trade.
- **Per-surface HTTP handling.** Status quo. The goal exists because it produced
  the query-loss, OPTIONS, TRACE, and asset-integrity defects.
- **Duplicated module UI.** Keeping two workbenches in sync was already failing;
  the shared component is the fix, not an option.

## Consequences

- Modules are usable from three places with identical semantics: console,
  `cli ui`, and any local script (`curl` against the published `module_api_url`).
- The agent process now owns a second listener. Its lifetime follows the module
  subsystem: `Agent.Close()` shuts it down before the invoker, supervisor, and IPC;
  tunnel reconnects do not affect it; a restart publishes a fresh epoch.
- The console's URLs and response schemas are unchanged (gate 4); its workbench
  became the shared component, so behaviour fixes land once.
- `cli ui` never trusts a stale endpoint: the URL is re-read and re-validated on
  every proxied request, and the page hides the registry when the running agent
  published none.
- Rollback is binary-only: no migration, no schema change, no new configuration
  key.

## Evidence

The implementation and its tests are the durable evidence. Development gate
transcripts from the original goal are intentionally kept out of the repository
and are not required to understand or verify this decision.
