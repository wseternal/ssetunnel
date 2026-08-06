# Gate: Test Coverage

## Condition
All new code paths have test coverage. All existing tests continue to pass. Tests run cleanly with `-race` flag.

## Evidence Required
- [ ] `go build ./...` succeeds
- [ ] `go test -race ./... -timeout 120s` passes (all packages)
- [ ] `go vet ./...` clean
- [ ] New tests for: InputAck write/parse round-trip, InputAck SSE forwarding, deferred capture timer behavior
- [ ] No orphaned TODOs or untested error paths in new code

## Verification Method
Run full test suite. Review test output for coverage of new code paths.

## Owner
Engineer + Test Engineer
