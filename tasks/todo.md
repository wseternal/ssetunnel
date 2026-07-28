# Todo List - ssetunnel Cycle 3

- [x] Slice 1: Database Schema & Migrations (`schema.hcl`, `atlas.hcl`, `migrations/`)
  - Acceptance: `schema.hcl` defines `tokens`, `pins`, `admin_sessions`; migration SQL files generated in `migrations/` and embedded via `//go:embed`.
  - Verify: `atlas migrate validate` / `go test ./migrations/...` passes.
  - Files: `schema.hcl`, `atlas.hcl`, `migrations/migrations.go`

- [x] Slice 2: Auth Storage Engine (`internal/auth/`)
  - Acceptance: `store.go` implements PostgreSQL operations with SHA-256 digest hashing & `sync.Map` read-through cache; `totp.go`, `pin.go`, `token.go` pass unit tests.
  - Verify: `go test ./internal/auth/... -race -count=1` passes with testcontainers (`postgres:tc:`).
  - Files: `internal/auth/store.go`, `internal/auth/totp.go`, `internal/auth/pin.go`, `internal/auth/token.go`, `internal/auth/store_test.go`

- [x] Slice 3: Server Middleware & HTTP Auth Enforcement (`internal/server/`)
  - Acceptance: `/events` and `/up` endpoints reject unauthorized requests with 401 when auth enabled, and bypass when `--disable-auth`.
  - Verify: `go test ./internal/server/... -run Auth -race -count=1` passes.
  - Files: `internal/server/middleware.go`, `internal/server/handlers.go`, `internal/server/server.go`

- [x] Slice 4: TCP Entry Listener Handshake & Buffer Pool (`internal/server/`)
  - Acceptance: Entry listener validates `<token>\n` within 5s timeout, replies `OK\n`, and uses `sync.Pool` 32KB buffers for zero-alloc `io.CopyBuffer` stream proxying.
  - Verify: `go test ./internal/server/... -run EntryHandshake -race -count=1` passes.
  - Files: `internal/server/server.go`, `internal/server/session.go`

- [x] Slice 5: Management JSON API (`internal/consoleapi/`)
  - Acceptance: `/api/v1/login`, `/api/v1/tokens`, `/api/v1/enroll`, `/api/v1/sessions` function correctly with admin TOTP cookie auth and in-memory session stats.
  - Verify: `go test ./internal/consoleapi/... -race -count=1` passes.
  - Files: `internal/consoleapi/router.go`, `internal/consoleapi/consoleapi_test.go`

- [x] Slice 6: Embedded React Console SPA & litespaserver Integration (`frontend/`, `internal/consoleserver/`)
  - Acceptance: React 18 + Vite + MUI console builds into `frontend/console/dist`, embeds via `frontend/frontend.go`, and is served via `litespaserver`.
  - Verify: `cd frontend/console && npm run build` succeeds; `go test ./internal/server/... -race -count=1` passes.
  - Files: `frontend/console/...`, `frontend/frontend.go`, `internal/consoleserver/consoleserver.go`

- [x] Slice 7: Connect Client Wrapper & Subcommand Integration (`internal/connect/`, `cmd/`)
  - Acceptance: `ssetunnel connect` supports Local Port and Stdio (`--local -`) modes, injecting `<token>\n` handshake; `agent` passes token in HTTP headers.
  - Verify: `go test ./internal/connect/... -race -count=1` passes; `go test ./cmd/ssetunnel/... -race -count=1` passes.
  - Files: `internal/connect/client.go`, `internal/transport/conn.go`, `internal/agent/agent.go`, `cmd/ssetunnel/main.go`

- [x] Slice 8: End-to-End Integration Tests (`internal/server/`)
  - Acceptance: Full E2E flow passes: Admin Login → PIN Generation → Agent Enrollment → Client Wrapper TCP Proxying.
  - Verify: `go test ./internal/server/... -run E2E_Cycle3 -race -count=1` passes.
  - Files: `internal/server/e2e_cycle3_test.go`
