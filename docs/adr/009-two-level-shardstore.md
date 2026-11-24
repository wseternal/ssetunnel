# ADR-009: Two-Level Shard Store

## Status

Accepted. This ADR records the standalone `internal/shardstore` package and its disk layout.

## Date

2026-11-24

## Context

`server.FileObjectStore` stores each module object at `<root>/<module-id>/<sha256(key)>`. Every object of one module shares one flat directory. ADR-008 accepted that layout for a bounded module workload.

The SDK also restricts object keys to flat ASCII names. A caller with arbitrary string keys cannot use the current backend. A large key set also concentrates many files in one directory, and that concentration adds directory lookup cost.

A need exists for a separate package with these properties:

- It accepts every non-empty Go string as a key.
- It spreads objects over a two-level directory tree.
- It reports the object distribution on disk.
- It does not replace the current backend.

## Decision

### 1. Standalone package

Add `internal/shardstore` with these files:

| File | Responsibility |
|------|-------------------|
| `path.go` | Key digest and relative path contract |
| `store.go` | Store lifecycle and CRUD |
| `stats.go` | Distribution inspection |

The package does not import the server, the SDK, or the control protocol. The server keeps `server.FileObjectStore` unchanged. No shared helper moves out of the server.

### 2. Path layout

`ResolvePath` computes the SHA-256 digest of the exact key bytes. It encodes all 32 bytes as lowercase hexadecimal. For digest `d`, it returns two relative paths:

```text
shardDir   = d[0:2] + "/" + d[2:4]
objectPath = shardDir + "/" + d
```

The public paths use slashes on every operating system. A filesystem call converts them with `filepath.FromSlash`. The package never uses a caller key as a path segment. An empty key returns `*EmptyKeyError` before any filesystem access.

### 3. Public contract

```go
func ResolvePath(key string) (shardDir, objectPath string, err error)

type EmptyKeyError struct{}

var ErrObjectNotFound error
var ErrStoreClosed error

func NewStore(rootDir string) (*Store, error)
func (s *Store) Close() error
func (s *Store) Put(key string, data []byte) error
func (s *Store) Get(key string) ([]byte, error)
func (s *Store) Delete(key string) error
func (s *Store) Exists(key string) (bool, error)

type ShardStats struct {
	LeafDirectories       int
	Objects               int
	MinObjectsPerLeaf     int
	MaxObjectsPerLeaf     int
	AverageObjectsPerLeaf float64
}

func NewShardInspector(store *Store) *ShardInspector
func (i *ShardInspector) Inspect() (ShardStats, error)
```

`Get` and `Delete` return `ErrObjectNotFound` through `errors.Is` for a missing object. `Exists` returns `(false, nil)` for a missing object. `Get` returns a non-nil empty slice for an empty object. Every method returns `ErrStoreClosed` through `errors.Is` after `Close`.

### 4. Store lifecycle

`NewStore` rejects an empty root path. It creates the root and its parent directories with mode `0700`. It opens one `os.Root` and keeps it for every call. It creates no shard directory.

`Close` releases the root. Close is idempotent. `Close` takes the write lock, and every CRUD call takes a read lock for its full operation. `Close` waits for active calls. No call starts after the root closes.

### 5. Writes

`Put` creates only the selected leaf directory. The mode is `0700`. The call sequence is:

1. Create a leaf directory with `os.Root.MkdirAll`.
2. Create a unique temporary file with `O_CREATE|O_EXCL` and mode `0600`.
3. Write all bytes, sync, and close the file.
4. Rename the temporary file to the full digest name in the same leaf.
5. Remove the temporary file after any failure.

The temporary name uses 8 random bytes from `crypto/rand`. `Put` does not remove the old object before the rename. A reader sees the old object or the new object, never a partial write.

`Put` adds one file sync per object. It adds no directory syncs, caches, or per-shard locks. `os.Root.MkdirAll` is idempotent, so two writers to one new leaf both succeed. `Put` has no object size limit.

### 6. Reads and deletes

`Get`, `Exists`, and `Delete` call `Lstat` first and reject a symlink or any other non-regular entry. `Delete` removes the object after that check and maps a missing path to `ErrObjectNotFound`.

The package trusts the operator-owned root. The `Lstat` and open pair in `Get` has a check-to-use window. A local writer needs write access to the `0700` root to use that window. ADR-008 accepts the same trust model.

### 7. Distribution inspection

`ShardInspector` reads one `Store`. `Inspect` takes the store write lock for the full scan, so it never observes a state that mixes its own calls. A separate process can still change the tree, because the root belongs to the operator.

`Inspect` walks the tree from the root and reads each directory once. It skips a branch when either level is not two lowercase hexadecimal characters. At each valid level-two directory, it counts only regular files with these properties:

- The name has 64 lowercase hexadecimal characters.
- The first two characters match the first directory name.
- Characters two through four match the second directory name.

The scan ignores temporary files, malformed names, non-regular entries, empty leaves, and files at other depths. It counts each leaf without a global object index. Empty leaves do not change the statistics. An empty tree returns zero for every field. `AverageObjectsPerLeaf` is `float64(Objects) / float64(LeafDirectories)`.

## Consequences

### Positive

- Arbitrary non-empty string keys reach a stable digest layout.
- One flat namespace spreads over 65,536 leaf directories without upfront creation.
- Atomic rename keeps the old or the new object visible during replacement.
- The package has no dependency on the server, the SDK, or the protocol.
- Current server behavior and data stay unchanged.

### Negative

- One file sync per write dominates large write workloads.
- The package has no list, size limit, retention, or quota.
- The inspector locks the store for its full scan, so long scans delay writes.
- Atomic replacement depends on platform rename behavior. The test evidence covers macOS only.
- The package does not serve current module objects. That needs a separate migration design.

## Rejected alternatives

- **Replace `server.FileObjectStore` now:** This changes deployed behavior and needs data migration.
- **Extract shared helpers from the server:** One new consumer does not justify a shared abstraction and the server regression risk.
- **Use one exclusive lock for every mutation:** `os.Root` supports concurrent calls. Idempotent directory creation and atomic rename supply the required safety.
- **Create all 65,536 leaves at startup:** This breaks lazy creation and adds startup cost.
- **Remove the file sync:** Atomic visibility does not need it. The current backend durability behavior is worth keeping.
- **Open a new root for each call:** Repeated file descriptors and path checks add cost without better confinement.
- **Keep the resolver unexported:** The package contract names path resolution, and external tests need the public form.

## References

- [ADR-008](008-module-object-store.md) records the current module-scoped backend.
