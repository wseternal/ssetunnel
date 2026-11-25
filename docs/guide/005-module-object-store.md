# Module Object Store

This guide explains how operators configure the module object store and how module authors use it. The store keeps module binary objects on the server local filesystem. Structured records stay in PostgreSQL through `sdk.DataStore`.

## Configure the server

Set the storage root with one flag:

```text
--object-store-dir string
    Server-local module object storage root (empty = disabled)
```

Run the server in the foreground:

```bash
ssetunnel server --object-store-dir=/var/lib/ssetunnel/objects
```

Install or start it as a service:

```bash
ssetunnel server start --object-store-dir=/var/lib/ssetunnel/objects
ssetunnel server status
```

The default is empty. Object storage is disabled in that case. Object storage needs no PostgreSQL and works with `--disable-auth`.

The server creates the root when it does not exist. An invalid root fails startup before any listener serves. The server fails startup when the path is a regular file or the process cannot use it.

### Permissions

The server sets these modes:

| Path | Mode |
|------|------|
| Root | `0700` |
| Module directory | `0700` |
| Object file | `0600` |
| Temporary file | `0600` |

Run the service as a user that owns the root. A service user without access fails startup or write operations.

## Use the store from a module

```go
import (
    "context"
    "errors"

    "github.com/wseternal/ssetunnel/sdk"
)

store, err := sdk.NewObjectStore()
if err != nil {
    return err
}

err = store.Put(ctx, "com.example.notes", "report.bin", payload)
if err != nil {
    return err
}

data, err := store.Get(ctx, "com.example.notes", "report.bin")
if errors.Is(err, sdk.ErrObjectNotFound) {
    return nil, err
}
if err != nil {
    return nil, err
}
```

`NewObjectStore` reads the module environment:

| Variable | Purpose |
|----------|---------|
| `SSETUNNEL_MODULE_IPC_ENDPOINT` | Agent IPC endpoint |
| `SSETUNNEL_MODULE_IPC_CREDENTIAL` | Launch credential |
| `SSETUNNEL_MODULE_ID` | Module identity |

The client presents its configured module ID on every call. The agent replaces any request module ID with the authenticated IPC identity. A call that passes a different module ID returns an error before any network I/O.

`NewObjectStoreWithEndpoint(endpoint, credential, moduleID)` accepts explicit values. Both constructors validate the configuration and perform no network I/O.

Each call opens one authenticated IPC connection. The call closes the connection before it returns.

### Contract

- `Put` replaces an existing object. The last writer wins.
- `Get` returns the exact stored bytes.
- `Get` returns a non-nil empty slice for a found empty object.
- `Get` returns `sdk.ErrObjectNotFound` for a missing object.

### Limits

| Limit | Value |
|-------|-------|
| Object size | 4 MiB |
| Key length | 128 bytes |
| Key form | `[A-Za-z0-9][A-Za-z0-9._-]*` |
| Control chunk | 32 KiB raw |
| Active transfers per agent | 8 |
| Active transfers per session | 8 |
| IPC frame | 6 MiB |
| Peak object memory per session | About 148 MiB (server: 68 MiB decoded; agent: 80 MiB of decoded buffers and encoded bodies) |

Keys are flat. A key cannot contain a path separator, whitespace, a control byte, or a traversal form.

A module that exceeds the transfer slots receives `busy` for the extra call. The request body is drained before the rejection, so the client always sees the error and never a reset connection. Callers should treat `busy` as retryable.

### Errors

| Code | Meaning |
|------|---------|
| `not_found` | The object does not exist |
| `invalid_request` | Bad key, bad module ID, bad chunk, or bad metadata |
| `too_large` | The object is larger than 4 MiB |
| `busy` | A transfer slot or the disk queue is full |
| `unavailable` | Object storage is disabled or the control stream is down |
| `internal` | A backend failure |

The SDK maps `not_found` to `sdk.ErrObjectNotFound`. Other codes become plain errors.

### AI automation proxy integration

`ai-automation-proxy` stores attachment bytes here. Start the server with
`--object-store-dir` before you submit a job with a file; a submit that carries a file fails
with `FILE_STORE_UNAVAILABLE` when storage is disabled.

The module applies these rules:

- One attachment is limited to 4 MiB of decoded bytes. An empty, malformed, or oversized file is
  rejected: `INVALID_FILE` (400) or `FILE_TOO_LARGE` (413).
- The bytes are gzip-compressed when that shrinks them. The stored object is then smaller than
  the file, and `file.encoding` in the job says `gzip`; otherwise the bytes are stored as
  submitted and `file.encoding` is absent.
- The object key is content-addressed: `file-<sha256-hex>` or `file-<sha256-hex>.gz`. Two jobs
  with the same file share one object.
- The job record holds `object_key`, `sha256`, and `byte_length`, never the base64 payload.
- The extension poll reads the object, checks the digest and length, and restores
  `data_base64`. A transport error is retried within a 5-second delivery budget, with at most
  three read attempts in total; a missing object or a failed check rejects the job with
  `FILE_UNAVAILABLE`.
- MIME filtering happens in the extension, not in the module. A submit can succeed for a type
  the extension rejects at dispatch, and that object remains until an operator removes it.

## Filesystem layout

```text
<root>/<module-id>/<sha256-hex-of-key>
```

The server hashes each key with SHA-256. The hash keeps platform filename rules and key characters out of the file path.

Example:

```text
/var/lib/ssetunnel/objects/com.example.notes/9f2c...c41
```

The logical key `report.bin` is not visible on disk. To find an object, use the SDK.

## Durability and partial writes

`Put` uses these steps:

1. Check the size and key.
2. Create a unique temporary file in the module directory.
3. Write the bytes.
4. Sync the file.
5. Close the file.
6. Rename the file in the same directory.

A reader sees the old complete object or the new complete object. A failed write removes the temporary file. The previous object stays in place.

`Get` rejects symlinks, non-regular files, and files above 4 MiB.

The backend trusts the root directory. A local writer who can modify the root can redirect a module namespace, and `Get` has a small window between its check and its open. Keep the root owned by the service user with mode `0700`. Do not give other users write access to it.

## Inspect metrics

The console exposes object-store counters to an admin:

```bash
curl -H "Authorization: Bearer <admin-token>" \
  http://127.0.0.1:8081/console/api/v1/metrics/objectstore
```

The response holds these fields:

| Field | Meaning |
|-------|---------|
| `put_total`, `get_total` | Completed operations |
| `put_errors`, `get_errors` | Backend failures |
| `not_found` | Expected misses, kept out of `get_errors` |
| `busy_rejections` | Full transfer slots or full disk queues |
| `bytes_in`, `bytes_out` | Bytes written and read |
| `latency_avg_ms`, `latency_samples` | Average backend latency |

Counters are server-scoped and reset when the server restarts.

## Backup and restore

Stop the server before a backup. Then copy the whole root:

```bash
ssetunnel server stop
tar -C /var/lib/ssetunnel -czf objects-backup.tar.gz objects
```

The archive keeps directory modes and file modes. PostgreSQL holds no object bytes. Back up the database separately for module records.

Restore these items:

1. Stop the server.
2. Replace the root with the archived directory.
3. Start the server with the same `--object-store-dir`.

## Disable the store

Start the server without the flag:

```bash
ssetunnel server start
```

Existing object files stay on disk. Modules receive `unavailable` for object calls. Structured records continue to work through `DataStore`.

## Roll back

1. Stop the server.
2. Install the previous server binary.
3. Keep the object root on disk.

A server without `--object-store-dir` ignores the root. A later version can use it again.

## Upgrade order

Use this order:

1. Upgrade the server.
2. Upgrade the agents.
3. Upgrade the modules.

An old agent treats `object_store_resp` as an unknown control message. Module object calls fail in that mixed state. This project does not claim mixed-version support for the object store.

## V1 exclusions

This version does not provide these items:

- Object listing or delete.
- Retention jobs or garbage collection.
- Per-module quotas.
- S3 or any network object store.
- Replication or multi-server consistency.
- Console or frontend behavior.

## Evidence limits

The filesystem smoke test ran on macOS with APFS. It covered empty, binary, 4 MiB, missing, overwrite, reopen, traversal, symlink, permission, oversized, and failed-write cases.

The atomic rename guarantee depends on the platform. Windows rename-over-existing behavior is not exercised. Treat non-macOS filesystem behavior as unverified until a test covers it.
