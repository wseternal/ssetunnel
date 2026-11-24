# ADR-008: Module Object Store

## Status

Accepted. This ADR records the module object store design and its trust model.

## Context

Modules persist structured records through `sdk.DataStore` and the control tunnel. The system had no binary storage. A module that needs to keep attachment bytes had two bad options:

1. Store the bytes in `module_records`. This mixes structured records and binary data in one table.
2. Get filesystem or database credentials. This breaks the rule that modules reach data only through the agent.

`ai-automation-proxy` shows the gap. It removes attachment bytes from persisted job history today.

## Decision

### 1. Public contract

Add `sdk.ObjectStore` with two methods:

```go
Put(ctx context.Context, moduleID, key string, data []byte) error
Get(ctx context.Context, moduleID, key string) ([]byte, error)
```

Add these limits and the not-found sentinel:

| Item | Value |
|------|-------|
| `MaxObjectSize` | 4 MiB |
| `MaxObjectKeyLen` | 128 bytes |
| `ErrObjectNotFound` | sentinel error |

Keys are flat. A key matches `[A-Za-z0-9][A-Za-z0-9._-]*`. The store rejects path separators, whitespace, control bytes, and traversal forms before any storage access.

`Get` returns a non-nil empty slice for a found empty object. A missing object returns an error that satisfies `errors.Is(err, sdk.ErrObjectNotFound)`. `Put` replaces an existing object and uses last-writer-wins semantics.

### 2. Filesystem backend

`server.FileObjectStore` uses this physical layout:

```text
<root>/<module-id>/<sha256-hex-of-key>
```

The hash keeps platform filename rules out of the layout. The backend has these properties:

- `os.Root` confines every operation to the configured root.
- The backend creates the root and module directories with mode `0700`.
- It creates temporary files and object files with mode `0600`.
- `Put` writes one unique `O_CREATE|O_EXCL` temporary file in the module directory. It syncs and closes the file, then renames it in the same directory. A reader sees the old object or the new object, never a partial write.
- A failed write removes the temporary file and preserves the previous object.
- `Get` checks `Lstat` first. It rejects symlinks and non-regular files.
- `Get` reads at most `MaxObjectSize + 1` bytes. It rejects an externally enlarged object.
- A missing file maps to `ErrObjectNotFound`.
- A read-write lock orders every operation against `Close`. `Close` waits for active calls and rejects new ones.
- The backend trusts the operator-owned root. A local writer who can modify the root can retarget a module namespace. The `Lstat`-then-open pair in `Get` also has a time-of-check to time-of-use window. The root mode denies both cases to a non-owner.

### 3. Transport

The SDK opens one authenticated IPC connection per operation. The protocol is:

```text
AUTH <len>\n<credential>
OK\n
OBJECTSTORE <module-id>\n
OK\n
OBJECTSTORE <len>\n<json request>
OBJECTSTORE_RESP <len>\n<json response>
```

The identity acknowledgment is mandatory. The module sends no request frame until it has read `OK`. A rejection during the identity check is delivered as a framed error while no request body is in flight. Once a request frame is in flight, the agent must drain the body before sending a `busy` rejection: closing with unread bytes resets the connection and the module sees a broken pipe instead of the error.

The ObjectStore IPC frame ceiling is 6 MiB. This covers the base64 form of a 4 MiB object plus bounded metadata. The agent reads the length header with a bounded line read before it allocates the body. The agent holds one of eight transfer slots for each request body. A full slot set returns `busy` after the body is drained.

The existing control tunnel carries the transfer. Add `object_store_req` and `object_store_resp` in `internal/moduleproto`. Do not change the 64 KiB control codec.

Every chunk repeats immutable transfer metadata:

| Field | Purpose |
|-------|---------|
| `request_id` | Correlation |
| `op` | `put` or `get` |
| `module_id` | Namespace |
| `key` | Object name |
| `sequence` | Chunk order |
| `chunk_count` | Total chunks |
| `final` | Last chunk flag |
| `total_size` | Object size |
| `sha256` | Digest |
| `data` | 32 KiB raw chunk |

A 4 MiB object has 128 chunks. An empty object has one final zero-data chunk and the empty SHA-256 digest. The receiver rejects mismatched metadata, duplicate chunks, gaps, wrong lengths, trailing data, and a bad digest.

The agent replaces the request `module_id` with the authenticated IPC identity. It discards any caller-supplied `request_id`, generates a fresh random `request_id`, rejects a duplicate ID already pending, and stores the control generation with each pending operation. A stale control stream fails only its own operations and rejects requests that try to register after that generation closed.

The SDK client presents its configured module ID on every call. A caller that passes a different module ID gets an error before any network I/O. The configured identity is authoritative.

A failed control write fails the pending operation at once. The module does not wait for the request timeout or for generation cleanup.

### 4. Server lifecycle

One `ObjectStoreHandler` serves each session. The control read loop performs decode, validation, and reassembly only. One worker goroutine per session performs backend disk calls. The worker queue holds eight operations. A full queue returns `busy`. Upload assemblies are keyed by module ID and request ID, so two modules cannot collide on a request ID. A one-minute ticker in the worker sweeps expired assemblies.

A get request carries no payload. The handler rejects a get that has a nonzero sequence, a chunk count other than one, a false final flag, a nonzero size, a digest, or data.

Server-scoped object-store metrics count completed puts and gets, misses, backend errors, busy rejections, bytes moved, and average backend latency. The console exposes them to an admin at `GET /api/v1/metrics/objectstore` on the console port.

A session holds at most eight upload assemblies. Eight decoded objects hold 32 MiB. Eight encoded IPC bodies hold another 48 MiB, so one session can hold about 80 MiB at peak. The agent holds its own copy of the encoded set. Incomplete assemblies expire after five minutes; the worker ticker reclaims them even when the session performs only reads.

The handler returns these structured error codes: `not_found`, `invalid_request`, `too_large`, `busy`, `unavailable`, and `internal`. Disabled storage returns `unavailable` with the matching request ID, so a caller never waits out a timeout.

Session close cancels the worker, waits up to five seconds for it to exit, and releases assemblies. The worker exits after its active filesystem call returns. A worker that outlives the wait cannot race the root close: the backend lock orders the two.

### 5. Configuration

Add the server flag:

```text
--object-store-dir string
    Server-local module object storage root (empty = disabled)
```

The default is empty, so existing installations keep object storage disabled. The server builds the backend before any listener serves and outside the authentication and PostgreSQL block. An invalid configured root fails startup. The root closes during server teardown. Object storage works with `--disable-auth` and does not need PostgreSQL.

### 6. Trust model

The IPC server uses one shared launch credential for all modules. It checks that the claimed module ID is registered. It cannot prove that a hostile registered module process owns that identity. ADR-006 accepts this model for `DataStore`.

This ADR confirms the same model for the object store. The agent overwrites the JSON `module_id` with the identity from the IPC connection. A hostile registered module can still select another registered identity during the IPC handshake. The object store does not claim isolation against that case. Per-module credentials need a separate design.

## Consequences

### Positive

- Binary objects no longer compete with structured records for table space.
- Object bytes stay on the server and reach modules only through the agent.
- The backend needs no external service and no credentials.
- Memory is bounded per request, per transfer, and per session.
- `DataStore`, the schema, and the migration set stay unchanged.

### Negative

- Bulk object traffic shares control bandwidth with resize, log, and catalog messages.
- V1 has no list, delete, retention, quota, replication, or cloud backend.
- One shared IPC credential limits isolation to trusted modules.
- Atomic replacement depends on platform rename semantics. The smoke evidence covers macOS only.

## Rejected alternatives

- **Store blobs in `module_records`:** This mixes structured and binary storage and changes DataStore behavior.
- **Send one base64 control message:** A 4 MiB object exceeds the 64 KiB frame limit.
- **Raise the control frame limit:** This increases peer allocation and head-of-line blocking for every control feature.
- **Open a dedicated yamux binary stream:** This adds routing, cancellation, and reconnect lifecycle beyond V1.
- **Generalize the DataStore transport:** Two use cases do not justify a shared abstraction and the added PostgreSQL risk.
- **Keep one persistent SDK client:** No latency gate requires it. One connection per operation removes client state and reconnect logic.
- **Add streaming backend methods and per-transfer goroutines:** Bounded 4 MiB objects do not need a second API. One worker per session is enough.
- **Enable a default storage directory:** Explicit opt-in avoids new directories and permission failures in existing installations.
