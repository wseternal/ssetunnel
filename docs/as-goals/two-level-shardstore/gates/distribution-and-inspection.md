# Gate: Distribution and Inspection

## Condition
Ten thousand sequential keys populate at least 2,000 distinct leaf directories. The inspector counts 10,000 objects. No object file resides at depth zero or depth one.

The inspector reports active leaf directories, total objects, minimum objects, maximum objects, and average objects per populated leaf. Empty leaves do not affect the statistics. An empty tree returns zero for every statistic. A controlled fixture verifies exact counts and averages.

## Evidence Required
- [ ] Inspector implementation -> `internal/shardstore/stats.go`
- [ ] Exact statistics and empty-tree tests -> `internal/shardstore/stats_test.go`
- [ ] Sequential-key distribution and zero-flat-storage checks -> `internal/shardstore/stats_test.go`
- [ ] Focused race-test results -> `docs/as-goals/two-level-shardstore/[iteration]/evidence-manifest.md`

## Verification Method
Run `go test -race ./internal/shardstore -run 'TestShardInspector|TestShardDistribution'`. Verify the controlled fixture independently. Store keys `user-1` through `user-10000`. Check the leaf threshold, object total, and depth invariants. Recompute the average from total objects and active leaves.

## Owner
Senior Software Engineer produces the evidence. System Test Engineer assigns the verdict.
