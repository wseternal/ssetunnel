# Module Protocol (moduleproto)

Wire protocol types, control message formats, and protocol limits for the agent module system.

## Contents

### Protocol Types (`protocol.go`)
- Control message types: `MsgModuleOpen`, `MsgModuleClose`, `MsgModuleSnapshotStart`, `MsgModuleSnapshotChunk`, `MsgModuleSnapshotCommit`.
- Payload structs for module open/close, catalog snapshot transfer.
- Catalog snapshot types: `ModuleEntry`, `HealthEntry`, `UIEntry`, `OperationSummary`.
- `ModuleStreamMarker` (`__module__\n`) prefix for invocation streams.

### Protocol Limits (`limits.go`)
Bounds that prevent runaway allocation from misbehaving peers:

| Constant | Value | Purpose |
|----------|-------|---------|
| `MaxModulesPerAgent` | 64 | Module count cap |
| `MaxModuleIDLen` | 128 | Reverse-DNS ID cap |
| `MaxOpenAPISize` | 1 MiB | OpenAPI document cap |
| `MaxCatalogSize` | 4 MiB | Aggregate catalog cap |
| `SnapshotChunkSize` | 32 KiB | Snapshot chunk size |
| `MaxActiveInvocations` | 32 | Per-agent invocation cap |
| `MaxActiveInvocationsPerModule` | 8 | Per-module invocation cap |
| `EstablishmentDeadline` | 3 s | Stream establishment timeout |

## Rules
- Limits are shared between agent and server — both sides enforce the same bounds.
- Snapshot protocol: `Start` → `Chunk` (0..N) → `Commit` (with SHA-256 digest).
- The `sdk/` package defines its own copies of these constants (intentional isolation — see S2 deferred finding).
