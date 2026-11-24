# Gate: CRUD and Concurrency

## Condition
The store creates its root without any shard directories. `Put` creates only the required two-level path. `Get` returns exact bytes, including a non-nil empty slice for an empty object. `Put` replaces existing data. `Exists` reports presence without an error for a missing key. `Delete` removes an object.

`Get` and `Delete` return a shared not-found error for missing objects. Parallel operations pass the race detector. Parallel writes to one new leaf both succeed. Concurrent readers never observe partial object content.

## Evidence Required
- [ ] Store implementation -> `internal/shardstore/store.go`
- [ ] Lazy layout, CRUD, missing-key, and concurrency tests -> `internal/shardstore/store_test.go`
- [ ] Atomic-write and directory-race review -> `docs/as-goals/two-level-shardstore/[iteration]/review.md`
- [ ] Focused race-test results -> `docs/as-goals/two-level-shardstore/[iteration]/evidence-manifest.md`

## Verification Method
Run `go test -race ./internal/shardstore -run 'TestStore'`. Inspect the root before the first write. Exercise empty, binary, overwritten, deleted, and missing objects. Start parallel writers against one known shard prefix. Combine parallel reads, writes, existence checks, and deletes. Verify final objects and temporary-file cleanup.

## Owner
Senior Software Engineer produces the evidence. Security Engineer reviews write integrity. System Test Engineer assigns the verdict.
