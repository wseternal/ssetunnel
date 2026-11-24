# Two-Level Shard Store

## Goal
Go callers can store non-empty arbitrary string keys without flat-directory hotspots and inspect object distribution on a local filesystem.

## Context
The current `server.FileObjectStore` hashes each key. It stores all objects for one module in one flat directory. This goal adds the requested standalone package. It does not replace the current backend.

## Success Criteria
- Known keys resolve to exact SHA-256 paths at `<root>/<b0>/<b1>/<digest>`.
- Empty keys return a distinct error type.
- `Put`, `Get`, `Delete`, and `Exists` support arbitrary non-empty keys and concurrent calls.
- Missing objects return consistent errors from `Get` and `Delete`.
- Writes create only the required shard directories.
- No object file resides at depth zero or depth one.
- A 10,000-key sequential test populates at least 2,000 leaf directories.
- `ShardInspector` reports exact leaf, object, minimum, maximum, and average counts.
- `go test -race ./...` passes.

## Constraints
- Use SHA-256 and lowercase hexadecimal digests.
- Use `digest[0:2]` and `digest[2:4]` as the two directory levels.
- Use the full 64-character digest as the object filename.
- Never use a caller-supplied key as a filesystem path segment.
- Create leaf directories only during writes.
- Add `internal/shardstore/path.go`, `store.go`, and `stats.go`.
- Use real temporary filesystems in tests. Do not use mocks.
- Stop after Iteration 1 planning for user review.

## Out of Scope
- Replacement of `server.FileObjectStore` in server startup.
- Migration of current object data.
- SDK, transport, protocol, console, and frontend changes.
- Remote storage backends, replication, retention, and quotas.
- Performance benchmarks and latency targets.
- Upfront creation of all 65,536 leaf directories.

## Phase 1 Confirmation
The user's detailed prompt confirms this goal and scope. The prompt also sets the final stop at Phase 4, Iteration 1, Step 1.

## Created
2026-11-24T13:09:51Z
