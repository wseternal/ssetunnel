# Gate: Path and Key Isolation

## Condition
Known non-empty keys resolve to their exact SHA-256 shard and object paths. The digest and path separators match the specified format. Empty keys return a distinct error type. Arbitrary keys cannot select filesystem path segments or escape the two-level layout.

The path contract uses lowercase hexadecimal text. The shard path contains exactly two two-character components. The object path adds one 64-character filename.

## Evidence Required
- [ ] Resolver implementation -> `internal/shardstore/path.go`
- [ ] Exact digest vectors and typed empty-error assertions -> `internal/shardstore/path_test.go`
- [ ] Traversal, slash, space, and Unicode round trips -> `internal/shardstore/store_test.go`
- [ ] Focused race-test results -> `docs/as-goals/two-level-shardstore/[iteration]/evidence-manifest.md`

## Verification Method
Run `go test -race ./internal/shardstore -run 'TestResolvePath|TestStoreKeyIsolation'`. Compare exact known SHA-256 vectors. Use `errors.As` for the empty-key error. Walk the temporary root after arbitrary-key writes. Confirm every object is a regular file exactly three components below the root.

## Owner
Senior Software Engineer produces the evidence. Security Engineer verifies the confinement review. System Test Engineer assigns the verdict.
