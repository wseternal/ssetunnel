# Evidence Manifest — Iteration 1

## Gate Status

| Gate | Status | Evidence | Owner |
|------|--------|----------|-------|
| Connect Service Dispatch | ✅ Pass | `cmd/ssetunnel/main.go#L79-L87`, `cmd/ssetunnel/service.go#L95-L98` (reload reject), `service.go#L131-L140` (name extraction), `service.go#L153-L161` (stdio reject), `service_test.go#TestConnectServiceRequiresName`, `TestConnectServiceRejectsStdio`, `TestConnectServiceRejectsReload`, `TestConnectServiceNameRequiresValue` | Engineer |
| Named Service Identity | ✅ Pass | `cmd/ssetunnel/service.go#L165-L168` (svcName override), `service.go#L175` (svcConfig.Name), `service.go#L295-L296` (buildRunFn connect), `service_test.go#TestBuildServiceArgsConnect`, `TestBuildRunFnIncludesConnect` | Engineer |
| Build and Test Green | ✅ Pass | `go build ./...` (clean), `go vet ./...` (clean), `go test ./cmd/ssetunnel/... -run TestConnect -v` (6/6 pass), full suite (56/56 pass excluding pre-existing Docker-dependent + 30s timeout tests) | Engineer |

## Return Shipments (Failed Gates)

None.

## Code Quality Findings
- Critical: 0
- Warning: 0
- Suggestion: 0

## Commits Reviewed
- `b9d011b`: feat: add service commands (start/stop/restart/status/uninstall) for connect sub-command
