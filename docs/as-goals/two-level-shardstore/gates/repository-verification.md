# Gate: Repository Verification

## Condition
The complete repository passes the requested race suite on the final tree. The package and repository compile. Static analysis reports no new failure. Product documentation matches the final API and disk layout.

The implementation remains inside the confirmed package and documentation scope. Server startup, SDK contracts, and transport protocols remain unchanged.

## Evidence Required
- [ ] `go test -race ./...` result -> `docs/as-goals/two-level-shardstore/[iteration]/evidence-manifest.md`
- [ ] `go build ./...` result -> `docs/as-goals/two-level-shardstore/[iteration]/evidence-manifest.md`
- [ ] `go vet ./...` result -> `docs/as-goals/two-level-shardstore/[iteration]/evidence-manifest.md`
- [ ] Architecture record -> `docs/adr/009-two-level-shardstore.md`
- [ ] Scope and commit review -> `docs/as-goals/two-level-shardstore/[iteration]/review.md`

## Verification Method
Run all three commands on the final commit. Record exit codes and relevant output. Review the final diff for scope. Compare public Go comments and the ADR with the implementation. Record any Docker dependency failure. Do not claim a Pass without a successful command.

## Owner
Senior Software Engineer runs the commands. Documentation Engineer verifies the architecture record. System Test Engineer assigns the verdict.
