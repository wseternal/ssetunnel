# Module Protocol (moduleproto)

Wire protocol types, control message formats, and protocol limits for the agent module system.

## Contents

### Protocol Types (`protocol.go`)
- Control message types: `MsgModuleOpen`, `MsgModuleClose`, `MsgModuleSnapshotStart`, `MsgModuleSnapshotChunk`, `MsgModuleSnapshotCommit`.
- Payload structs for module open/close, catalog snapshot transfer.
- Catalog snapshot types: `ModuleEntry`, `HealthEntry`, `UIEntry`, `OperationSummary`.
- `ModuleStreamMarker` (`__module__\n`) prefix for invocation streams.

### Object Store (`objectstore.go`)
Chunk envelope for module object transfers.

- Message types: `MsgObjectStoreReq` (`object_store_req`) and `MsgObjectStoreResp` (`object_store_resp`).
- One `ObjectStoreMessage` shape carries `request_id`, `op`, `module_id`, `key`, `sequence`, `chunk_count`, `final`, `total_size`, `sha256`, `data`, and the error fields.
- `NewObjectStoreDataChunks` fragments a bounded object. `ObjectStoreAssembler` validates metadata, order, length, and the final SHA-256 digest.
- Error codes: `not_found`, `invalid_request`, `too_large`, `busy`, `unavailable`, `internal`.

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
| `ObjectStoreChunkSize` | 32 KiB | Object chunk size |
| `MaxActiveObjectTransfers` | 8 | Per-agent and per-session transfer cap |

## Rules
- Limits are shared between agent and server — both sides enforce the same bounds.
- Snapshot protocol: `Start` → `Chunk` (0..N) → `Commit` (with SHA-256 digest).
- Object store frames stay below the 64 KiB control frame limit. A 32 KiB raw chunk plus metadata measures under 44 KiB.
- Do not change the 64 KiB control codec for object store traffic. Fragment at the object layer instead.
- The `sdk/` package defines its own copies of these constants (intentional isolation — see S2 deferred finding).
