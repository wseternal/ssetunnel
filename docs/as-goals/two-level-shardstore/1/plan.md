# Iteration 1 Plan: Two-Level Shard Store

## Inputs

- Goal: `docs/as-goals/two-level-shardstore.md`
- Gates: `docs/as-goals/two-level-shardstore/gates/`
- Current backend: `internal/server/objectstore_fs.go`
- Stop condition: Complete this plan, then stop before Engineer implementation.

## Scope and Current State

`server.FileObjectStore` stores each module object at `<root>/<module-id>/<sha256(key)>`. Objects in one module share a flat directory. The current SDK also restricts keys to flat ASCII names.

This plan adds an isolated `internal/shardstore` package. The package accepts every non-empty Go string. It does not reuse the SDK key rules. It does not replace the server backend or read current object data.

## Critical Files

- `internal/server/objectstore_fs.go:19-63,69-181,198-214` — Reference for `os.Root`, atomic writes, file checks, and lifecycle.
- `internal/server/objectstore_fs_test.go:18-171` — Reference for real-filesystem and race tests.
- `internal/shardstore/path.go` — New digest and relative-path contract.
- `internal/shardstore/store.go` — New store lifecycle and CRUD contract.
- `internal/shardstore/stats.go` — New inspector contract and shard-tree traversal.

## Architecture Contract

### Public API

```go
func ResolvePath(key string) (shardDir, objectPath string, err error)

type EmptyKeyError struct{}

var ErrObjectNotFound error

type Store struct { /* unexported fields */ }

func NewStore(rootDir string) (*Store, error)
func (s *Store) Close() error
func (s *Store) Put(key string, data []byte) error
func (s *Store) Get(key string) ([]byte, error)
func (s *Store) Delete(key string) error
func (s *Store) Exists(key string) (bool, error)

type ShardStats struct {
    LeafDirectories    int
    Objects            int
    MinObjectsPerLeaf  int
    MaxObjectsPerLeaf  int
    AverageObjectsPerLeaf float64
}

type ShardInspector struct { /* unexported fields */ }

func NewShardInspector(store *Store) *ShardInspector
func (i *ShardInspector) Inspect() (ShardStats, error)
```

`ResolvePath` returns `*EmptyKeyError` for an empty key. Tests use `errors.As`. `Get` and `Delete` return an error that matches `ErrObjectNotFound` through `errors.Is`. `Exists` returns `(false, nil)` for a missing object.

`Close` releases the retained `os.Root`. It is idempotent. Every operation after close returns a closed-store error. The inspector also returns this error after store close.

### Path Resolution

Use SHA-256 over the exact key bytes. Encode all 32 bytes as lowercase hexadecimal. For digest `d`, return these slash-separated relative paths:

```text
shardDir   = d[0:2] + "/" + d[2:4]
objectPath = shardDir + "/" + d
```

Keep the public paths slash-separated on every operating system. Convert them with `filepath.FromSlash` at `os.Root` boundaries. Never pass the original key to a filesystem operation.

### Store Lifecycle and Writes

`NewStore` rejects an empty root path. It creates only the root and required parents with mode `0700`. It opens one `os.Root` and retains it for all operations. It creates no shard directories.

Each CRUD method holds `RLock` for its full operation. `Close` holds `Lock`. This design permits concurrent filesystem operations and prevents close races. `os.Root.MkdirAll` creates only the selected shard path with mode `0700`. Its idempotent behavior supports concurrent creation.

`Put` uses this sequence:

1. Resolve the key and create the selected leaf.
2. Create a unique `O_CREATE|O_EXCL` temporary file in the same leaf with mode `0600`.
3. Write all bytes, call `Sync`, and close the file.
4. Rename the temporary file to the full digest name in the same leaf.
5. Remove the temporary file after any failure.

Do not delete an old object before rename. A reader sees the old object or the new object. Do not add directory syncs, batching, caches, or per-shard locks.

`Get` and `Exists` use `Lstat` and reject non-regular entries. `Get` returns a non-nil empty slice for an empty object. `Delete` checks the same regular-file contract before removal. No object-size limit applies in this package.

### Inspector

`ShardInspector` refers to one `Store`. `Inspect` takes the store's exclusive lock for a stable view against package operations. It walks `Store.root.FS()` from `.`. External changes remain outside this lock, as the root is operator-owned.

Prune directories that do not match the expected level and lowercase hexadecimal form. At each valid depth-two directory, read its direct entries once. Count only regular files with these properties:

- The filename contains exactly 64 lowercase hexadecimal characters.
- The first two directory components each contain two lowercase hexadecimal characters.
- The filename prefix matches both directory components.

Ignore temporary files, malformed names, non-regular entries, empty leaves, and files at other depths. Compute each leaf count without a global object index. Update totals and per-leaf values after each leaf scan. Return zero values for an empty tree. Compute the average as `float64(Objects) / float64(LeafDirectories)`.

### Scope Guard

Do not change `internal/server`, `internal/command`, `sdk`, transport protocols, `go.mod`, or the frontend. Do not extract shared helpers from the server for this standalone package. Commit only the new package, its tests, and the product ADR. Never commit `docs/as-goals/`.

## Trade-Off Summary

| Perspective | Feasibility | Diff Footprint | Regression Risk | Primary Trade-Off |
|-------------|-------------|----------------|-----------------|-------------------|
| Simplicity | High | Small | Low | Exclusive mutation locks would reduce parallel writes. |
| Performance | High | Small | Low | One file sync per `Put` dominates large test workloads. |
| Minimal Change | High | Smallest | Lowest | Small helper duplication avoids server coupling. |

Use the minimal-change isolation model. Retain the performance review's single root and per-leaf aggregation. Retain the simplicity review's store-bound inspector and small concrete API. Use read locks for operations instead of exclusive mutation locks.

## Rejected Alternatives

- **Replace `server.FileObjectStore` now:** This expands the diff, changes deployed behavior, and requires data migration.
- **Extract shared server storage helpers:** One new consumer does not justify a shared abstraction or server regression risk.
- **Use an unexported resolver:** The spec names path resolution as a package deliverable. External tests need the public contract.
- **Serialize every mutation with one exclusive lock:** `os.Root` supports concurrent calls. Atomic rename and idempotent directory creation provide the required safety.
- **Pre-create all 65,536 leaves:** This violates lazy creation and adds startup cost.
- **Remove file sync to accelerate tests:** Atomic visibility does not require sync, but the current backend's durability behavior is worth preserving.
- **Open a new root for every operation:** Repeated file descriptors and path checks add overhead without improving confinement.

## Ordered Implementation Tasks

### Task 1: Resolve Exact Shard Paths

**Dependencies:** None.

**Files:**

- `internal/shardstore/path.go`
- `internal/shardstore/path_test.go`

**Implementation:**

- Add `EmptyKeyError` and `ResolvePath`.
- Use `crypto/sha256` and `encoding/hex`.
- Keep path construction pure and deterministic.
- Use the external test package `shardstore_test`.
- Write the tests before the implementation and confirm the initial failure.

**Test Cases:**

- `TestResolvePath` checks `hello` as `2c/f2/2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824`.
- `TestResolvePath` checks `user-1` as `c6/c2/c6c289e49e9c05b2145860387b73bcb18df43fb09a1e4a4a9713c76c88bb541b`.
- `TestResolvePath` checks `"\u043a\u043b\u044e\u0447"` as `1d/e3/1de36a32af798da0c1ac9297603a320ed8fe567cf21c9177112a4ce914ebb8be`.
- `TestResolvePathEmpty` checks the distinct error type with `errors.As`.
- Table checks confirm digest length, lowercase hexadecimal form, and repeatability.

**Acceptance Criteria:**

- [ ] Every non-empty key produces exactly two shard components and one digest filename.
- [ ] Known keys match the exact vectors above.
- [ ] Empty keys return `*EmptyKeyError` without filesystem access.

**Verification:**

```bash
go test -race ./internal/shardstore -run '^TestResolvePath'
```

### Task 2: Implement Lazy Atomic CRUD

**Dependencies:** Task 1.

**Files:**

- `internal/shardstore/store.go`
- `internal/shardstore/store_test.go`

**Implementation:**

- Add `Store`, `NewStore`, `Close`, and the four required operations.
- Add `ErrObjectNotFound` and a consistent closed-store error.
- Retain `*os.Root` and order all operations against `Close`.
- Use lazy `MkdirAll`, private modes, regular-file checks, and same-leaf atomic rename.
- Use `crypto/rand` for unique temporary suffixes. Return random-source errors.
- Keep all tests on real `t.TempDir` filesystems. Do not use mocks.
- Write the tests before the implementation and confirm the initial failure.

**Test Cases:**

- `TestStoreLazyShardCreation` verifies an empty root after open and only one required leaf after one write.
- `TestStoreCRUD` covers nil data, binary data, overwrite, existence, delete, and missing-key errors.
- `TestStoreKeyIsolation` covers `../escape`, `..`, `/leading`, `trailing/`, spaces, backslashes, and Unicode.
- `TestStoreKeyIsolation` walks the root and verifies every stored object is exactly three components deep.
- `TestStoreConcurrentSameLeafPut` writes `same-leaf-19` and `same-leaf-91` concurrently. Both keys resolve under `8c/b3`.
- `TestStoreConcurrentCRUD` runs parallel operations on unique keys. It also reads one key during atomic replacement with fixed payloads. Readers may see only a complete payload.
- `TestStoreClose` checks idempotent close and rejects later operations.
- Every test checks unexpected temporary files after the operations finish.

**Acceptance Criteria:**

- [ ] No shard exists before the first write.
- [ ] Arbitrary keys round-trip without path escape or flat object files.
- [ ] `Get` and `Delete` match `ErrObjectNotFound`. `Exists` returns `(false, nil)` for absence.
- [ ] Same-leaf creators and concurrent CRUD operations pass with `-race`.
- [ ] Readers never observe partial writes, and failures leave no temporary objects.

**Verification:**

```bash
go test -race ./internal/shardstore -run '^TestStore'
```

### Task 3: Add Distribution Statistics

**Dependencies:** Tasks 1 and 2.

**Files:**

- `internal/shardstore/stats.go`
- `internal/shardstore/stats_test.go`

**Implementation:**

- Add `ShardStats`, `ShardInspector`, `NewShardInspector`, and `Inspect`.
- Traverse the retained root and prune invalid branches.
- Count valid regular objects per populated leaf without a global object index.
- Ignore temporary files, malformed entries, empty leaves, and non-regular entries.
- Use the external test package and write tests first.

**Test Cases:**

- `TestShardInspectorEmpty` returns zero values for every field.
- `TestShardInspectorStats` uses three leaves with three, two, and one objects. It expects six objects, three leaves, minimum one, maximum three, and average two.
- The exact fixture uses digest prefixes `0000`, `0001`, and `ffff`. It also includes an empty leaf, a temporary file, and malformed entries for filter checks.
- `TestShardDistribution` stores `user-1` through `user-10000` through the public `Put` API.
- `TestShardDistribution` expects 10,000 objects and at least 2,000 active leaves.
- `TestShardDistribution` independently walks the root and rejects files at depth zero or depth one.
- `TestShardDistribution` checks the average against `10,000 / LeafDirectories` and checks minimum and maximum bounds.

**Acceptance Criteria:**

- [ ] The controlled fixture produces exact statistics.
- [ ] Empty leaves and invalid entries do not affect statistics.
- [ ] Sequential keys meet the distribution threshold and zero-flat-storage invariant.
- [ ] The empty tree returns all zero values.

**Verification:**

```bash
go test -race ./internal/shardstore -run 'TestShardInspector|TestShardDistribution'
```

### Task 4: Record the Architecture

**Dependencies:** Tasks 1 through 3.

**Files:**

- `docs/adr/009-two-level-shardstore.md`

**Implementation:**

- Document the standalone scope and the unchanged server backend.
- Record the exact path layout, public errors, lazy creation, and file modes.
- Record atomic visibility, file sync, close ordering, and platform rename limits.
- Record inspector filtering, empty-tree values, and lock behavior.
- Link ADR-008 as the current module-scoped backend design.

**Acceptance Criteria:**

- [ ] The ADR matches the final public API and implementation.
- [ ] The ADR states that current object data requires separate migration design.
- [ ] Public Go comments contain no stale flat-layout or SDK-key claims.

**Verification:**

```bash
go doc ./internal/shardstore
go test -race ./internal/shardstore
```

## Final Verification Checkpoint

Run these commands on the final committed code and documentation:

```bash
go test -race ./...
go build ./...
go vet ./...
```

Record all exit codes in `1/evidence-manifest.md`. Record any Docker dependency failure. Do not mark the repository gate Pass without a successful full command. Run the Test Engineer and Security Engineer reviews after implementation. Confirm that no server, SDK, command, protocol, or dependency file changed.

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Partial reads during replacement | Use same-leaf temporary files and atomic rename. Test fixed complete payloads. |
| Same-leaf creation races | Use idempotent `os.Root.MkdirAll` and a two-key fixed fixture. |
| Close during filesystem access | Hold read locks for full operations and the write lock for close. |
| Key traversal or platform filename rules | Hash every key and convert only digest paths for OS calls. |
| Temporary or malformed files skew statistics | Enforce depth, type, hexadecimal, length, and prefix checks. |
| Inspector observes mixed package states | Hold the exclusive store lock for the complete scan. |
| The 10,000-write test becomes slow | Retain one file sync per write. Add no extra syncs, scans, or benchmark machinery. |
| Cross-platform path differences | Return canonical slash paths and use `filepath.FromSlash` at OS boundaries. |
| Scope creep into deployed storage | Keep all runtime files unchanged and require a separate migration design. |

## Read-Only Planning Audit

The baseline before design review was `?? docs/as-goals/`. The audit after all three reviews returned the same status. No design agent changed a product file.

## Stop Point

This plan completes Phase 4, Iteration 1, Step 1. Do not start Step 2 until the user finishes document review and requests implementation.
