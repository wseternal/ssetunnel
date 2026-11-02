# Agent Module Registry

## Goal
Server-side consumers can discover and invoke HTTP services from separately managed agent modules, and console users can open module-provided pages.

## Context
The agent and server already share an SSE plus POST transport. Yamux carries data streams and one control stream. The control stream supports agent-level messages. The data streams support independent bidirectional byte flow. The agent currently supports fixed TCP targets, dynamic TCP targets, cloud shells, and remote applications. The console serves one embedded React SPA and has no dynamic module page host.

The project needs an agent-side registry for separate module processes. Providers need OpenAPI v3 YAML service contracts. Server-side consumers need discovery and invocation APIs. Providers and consumers need one shared Go SDK. The console needs a trusted Vite Module Federation host for provider or consumer UI modules.

## Success Criteria
- A local CLI can install, start, stop, and remove a module without an agent rebuild or restart.
- A provider can publish its identity, OpenAPI v3 YAML, UI metadata, and health through the registry.
- An authorized server consumer can discover the current services for a selected agent and module.
- An authorized consumer can invoke HTTP operations with headers, query values, bodies, streamed data, cancellation, and errors.
- One Go SDK supports provider registration, consumer discovery, consumer invocation, and typed error handling.
- The console can load a declared provider or consumer federation remote and route users to its page.
- A module crash or restart does not stop the agent or corrupt unrelated tunnel sessions.
- Reconnects replace stale registry state and restore current module advertisements.

## Constraints
- Modules run as separate OS processes and use local IPC with the agent registry.
- Local CLI commands control module installation and lifecycle in the first version.
- Service contracts use OpenAPI v3 YAML and HTTP semantics.
- Registry metadata and lifecycle messages use the existing yamux control stream.
- Each invocation uses a dedicated yamux data stream over the existing SSE plus POST transport.
- The design preserves existing TCP, shell, and remote application behavior.
- Console authorization preserves current user-scoped agent access rules.
- Console federation remotes come only from trusted installed module packages.
- Backend tests must not use mocks. Frontend tests require prior user permission.
- This run stops after the Phase 4 iteration-one plan. No implementation starts.

## Out of Scope
- Remote module installation, updates, or removal from the console.
- In-process Go plugins or shared-library loading.
- Non-HTTP interface definition languages.
- A public module marketplace or automatic dependency resolution.
- Automatic load balancing across agents for the same service.
- Sandboxing third-party JavaScript beyond the trusted package and allowlist model.
- Implementation, test execution, commits, and deployment during this planning run.

## User Confirmations
- Modules must load and unload without an agent rebuild or restart.
- Module APIs use HTTP semantics, including streamed bodies.
- The first version uses a local CLI for module installation and lifecycle control.

## Created
2026-11-02 Asia/Shanghai
