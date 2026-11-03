# ADR-004: Digest-Keyed Package Trust and UI Asset Federation

## Status
Accepted

## Date
2026-11-02

## Context

Modules (ADR-002) ship as packages containing an executable, an OpenAPI spec, and optionally a web UI. The console frontend needs to display module UIs (forms, dashboards, visualizations) that run within the admin SPA. This raises two security challenges:

1. **Package integrity**: how does the server verify that a module's executable and assets haven't been tampered with?
2. **UI trust**: the console SPA loads and executes JavaScript from modules. A malicious or compromised module UI could steal admin credentials or corrupt tunnel state.

The tunnel already serves a single-file React SPA (via `vite-plugin-singlefile` + `go:embed`). Module UIs must integrate without breaking this deployment model.

## Decision

Adopt a **two-level digest trust model**: packages are identified by their SHA-256 digest, and every UI asset within a package is individually digest-verified before caching and serving.

### Package Validation Pipeline

On installation, every package goes through:

1. **Module ID format**: reverse-DNS regex (`^[a-z][a-z0-9]*(\.[a-z][a-z0-9]*)+$`), max 128 chars
2. **Semver version**: validated against semver regex, max 32 chars
3. **Executable path**: must be absolute, within the package directory
4. **Symlink rejection** (C9 fix): `os.Lstat` rejects symlinked executables; `filepath.EvalSymlinks` resolves both executable and package directory before sub-path check. Prevents `evil-bin → /usr/bin/sudo` attacks.
5. **OpenAPI YAML**: size limit (1 MiB), parsed for validity
6. **UI descriptors**: each asset's path, size, and SHA-256 digest validated

### Trusted UI Asset Store

The server maintains an in-memory asset store (`moduleui.Store`) with:

| Bound | Value | Purpose |
|-------|-------|---------|
| Per-asset size | 8 MiB | Prevent single-asset memory exhaustion |
| Total cache size | 64 MiB | Bounded aggregate memory |
| Digest verification | SHA-256 | Every asset verified on write |

**Installation flow**:
1. Admin installs a `PackageManifest` declaring every asset's path, size, and SHA-256 digest
2. The store validates path safety (no `..`, no `.`, no absolute paths)
3. Assets are cached via `CacheAsset()` which verifies size and digest match the manifest

**Serving flow**:
- **Consumer assets** (server-installed UI): served from the trusted store at `/api/v1/modules/ui/consumer/{digest}/{path}`. The digest in the URL enables immutable caching (`Cache-Control: public, max-age=31536000, immutable`).
- **Provider assets** (agent-side UI): proxied through module invocation at `/api/v1/modules/ui/provider/{agent-id}/{module-id}/{path}`. Response body bounded by `MaxAssetSize` (8 MiB).

### Path Traversal Prevention

Asset paths are validated by `validateAssetPath()`:
- Must be relative (no leading `/`)
- No `..` or `.` components
- No empty segments
- Checked against the manifest's declared paths

### Console Integration

The frontend SPA adds a "Modules" tab with:
- Agent selector dropdown (lists connected agents)
- Module summary cards (ID, service name, version, health, UI availability)
- OpenAPI spec viewer (raw YAML display)
- Links to module UIs when `has_ui: true`

The modules API endpoints:
- `GET /api/v1/modules?agent=<id>` — list modules with summaries
- `GET /api/v1/modules/<agent-id>/<module-id>` — full detail with OpenAPI
- `GET /api/v1/modules/openapi/<agent-id>/<module-id>` — raw OpenAPI YAML

### Catalog Reassembly

The server receives catalog snapshots from the agent via the control stream:

1. **Start**: validates epoch, pre-allocates buffer (C12 fix — avoids O(n²) append)
2. **Chunks**: validates sequence, enforces size limits, checks 5-minute reassembly timeout (W16 fix)
3. **Commit**: concatenates chunks, verifies SHA-256 digest, parses entries, validates each OpenAPI doc (skip invalid), atomically replaces the previous catalog

The `ModuleCatalog` uses a worker pool (2 workers, queue depth 8) for async OpenAPI validation. Invalid OpenAPI docs are logged and the module is skipped — one bad module doesn't block the entire catalog.

### Session-Scoped Catalogs

Catalogs are **session-scoped, not global**:

- Each agent session has its own `ModuleCatalog`
- Catalogs are rebuilt from snapshots on every reconnect
- The transport session ID serves as the catalog epoch
- Stale epochs are rejected

This prevents cross-user leakage and stale-state reconciliation problems.

### Key Code Locations

| Component | File | Role |
|-----------|------|------|
| Package validation | `internal/agentmod/package.go` | ID format, symlink rejection, path containment |
| UI store | `internal/moduleui/store.go` | Digest-verified bounded cache |
| UI route handlers | `internal/consoleserver/moduleui_routes.go` | Consumer/provider asset serving |
| Catalog reassembly | `internal/server/module_catalog.go` | Three-phase commit, worker pool |
| Package validation (server) | `internal/server/module_packages.go` | Server-side package acceptance |
| Module routes | `internal/consoleserver/module_routes.go` | Discovery and invocation APIs |
| Consumer SDK | `sdk/discovery.go` | External client SDK for module access |
| Protocol limits | `internal/moduleproto/limits.go` | All size and count bounds |

## Alternatives Considered

### Arbitrary federation remote URLs
- **Pros**: Simple — point to any URL and load it
- **Cons**: Bypasses package trust entirely. Remote JS executes with console credentials. CORS configuration becomes a security surface.
- **Rejected**: Only declared, digest-verified assets from installed packages are served

### No digest verification
- **Pros**: Simpler installation (no manifest declaration)
- **Cons**: Any file modification (disk corruption, MITM, compromised agent) serves malicious code to the admin console
- **Rejected**: The trust model requires verification at every level

### Unbounded cache
- **Pros**: Simpler code, no eviction logic
- **Cons**: A module with thousands of assets exhausts server memory. No defense against malicious packages declaring excessive assets.
- **Rejected**: 64 MiB total, 8 MiB per asset — sufficient for dozens of modules

### Persistent global server catalog (PostgreSQL)
- **Pros**: Catalogs survive server restarts, queryable across sessions
- **Cons**: Stale-session reconciliation (which agent's catalog is current?), cross-user leakage risk, added database complexity for ephemeral data
- **Rejected**: Session-scoped in-memory catalogs are simpler and safer

### Sandboxed module UI (iframe, Web Components shadow DOM)
- **Pros**: Isolation from admin SPA state
- **Cons**: Adds complexity, breaks MUI theme sharing, iframe sandbox limits functionality, no standard approach for Vite Module Federation
- **Deferred**: The current trust model (digest-verified declared packages) provides sufficient security for the admin console context. Sandbox isolation can be added later if untrusted third-party modules are introduced.

### Full React Router migration for module tabs
- **Pros**: Standard routing, deep-linkable module URLs
- **Cons**: Large refactor of a 3575-line App.tsx, regression risk, no isolation benefit for module components
- **Rejected**: Tab-based navigation with module state is sufficient for the current UI scope

## Consequences

### Positive
- Two-level digest trust (package → asset) ensures only verified code runs in the console
- Symlink rejection closes arbitrary code execution via crafted packages
- Bounded cache prevents memory exhaustion from malicious or misbehaving modules
- Session-scoped catalogs eliminate cross-user leakage and stale-state reconciliation
- Immutable cache headers on digest-keyed URLs enable aggressive browser caching
- Worker pool for OpenAPI validation prevents one bad module from blocking the catalog
- Path traversal prevention stops `../` attacks through crafted asset paths

### Negative
- Admin must install package manifests before consumer UI assets can be served (extra deployment step)
- In-memory store is lost on server restart — manifests must be re-installed (acceptable for current deployment model)
- 8 MiB per-asset limit may be too restrictive for complex module UIs with large bundled assets
- Provider UI assets go through the full invocation path (yamux stream + IPC) for every request — no caching on the server side

### Risks
- Remote JavaScript from declared packages is treated as trusted code — a compromised package maintainer can push malicious updates (mitigated by the digest in the manifest requiring admin re-installation for updates)
- The 5-minute reassembly timeout (W16) may be too short for agents with very large catalogs on slow links — currently acceptable given the 4 MiB catalog limit
- No rate limiting on provider asset proxying — a misbehaving module could consume yamux streams with asset requests

## What's Still To Come
- Full Vite Module Federation runtime integration (dynamic `loadRemote` for module components)
- Module UI sandboxing (iframe or Web Worker isolation for untrusted third-party modules)
- Server-side caching for provider UI assets (avoid repeated invocations for static assets)
- Package registry service (centralized package distribution, eliminating per-server manifest installation)
