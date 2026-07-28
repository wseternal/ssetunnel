# Spec: ssetunnel — Cycle 3 (Auth, Management Console, Connect Wrapper)

> Base spec: `docs/specs/ssetunnel.md` (architecture, style, testing guidelines).
> Cycle 2 spec: `docs/specs/ssetunnel-cycle2.md` (upstream throughput & probe completed).
> Idea docs: `docs/ideas/ssetunnel.md`, `docs/ideas/ssetunnel-cycle2.md`.
> Pattern Reference: `/Users/jiangzhaohua/visdom/auth-go` (`orcacommon`, `testcontainers`, `atlas`, React+Vite+MUI, `litespaserver`).

## Objective

Deliver the security, administration, and user-access layer for `ssetunnel` following the established patterns in `auth-go`:

1. **Authentication & PostgreSQL Token Management (`internal/auth/`)**:
   - Enrollment via TOTP code (shared enrollment secret) or single-use PIN (8+ base32 chars, 15-min expiry, single-use).
   - Bearer token issuance with roles (`agent`, `user`, `admin`).
   - **PostgreSQL Storage**: Replaces file-based storage. Uses `github.com/visdomtech/orcacommon/postgres` pool helpers (`orcapostgres.OpenPool` / `orcapostgres.DBConfig`).
   - **Atlas Schema Management**: Declarative schema in `schema.hcl`, `atlas.hcl`, and versioned SQL migrations in `migrations/` directory embedded via `//go:embed` (`migrations.FS`).
   - **Local & Test Environment**: Uses `testcontainers` for isolated local integration testing (`DatabaseURLTemplate: "postgres:tc:"`).
   - Auth middleware enforcing bearer token validation on agent endpoints (`/events`, `/up`), user entry points, and management API routes.

2. **Embedded Management Console (`frontend/console/` & `internal/server/`)**:
   - **Tech Stack**: React 18 + Vite + Material UI (MUI - `@mui/material`, `@emotion/react`, `@emotion/styled`, `@mui/icons-material`), React Router, TanStack React Query.
   - Located at `frontend/console/`, built to `frontend/console/dist`, and exposed via `//go:embed` in package `frontend`.
   - **SPA Engine**: Powered by `github.com/visdomtech/orcacommon/litespaserver` (`litespaserver.NewServer(ctx, pool, config)`).
   - Admin login session using TOTP (HTTP-only session cookie with 12h validity).
   - Web interface for:
     - Enrolling new agents/users (TOTP QR code display & PIN generation).
     - Token lifecycle management (list active tokens, revoke tokens).
     - Live agent session status monitoring (active agents, connected users, uptime, traffic bytes).
   - JSON management API (`/api/v1/...`) powering the console.

3. **Connect Client Wrapper (`cmd/ssetunnel/main.go` & `internal/connect/`)**:
   - `ssetunnel connect` subcommand listening on local TCP (`--local <port>`).
   - Accepts local TCP connections (e.g., SSH, psql, curl) and connects to the server entry listener.
   - Handshake with user bearer token injection to authenticate and establish a stream to the target agent.
   - Compatible with `ProxyCommand` for SSH and standard DB/HTTP proxying.

---

## Tech Stack

- **Backend:** Go 1.26.5, module `github.com/wseternal/ssetunnel`
- **Backend Dependencies:**
  - `github.com/hashicorp/yamux` (multiplexing — existing)
  - `github.com/pquerna/otp` (TOTP generation and verification)
  - `github.com/visdomtech/orcacommon` (incorporating `orcacommon/postgres` and `orcacommon/litespaserver`)
  - `github.com/jackc/pgx/v5` (PostgreSQL driver)
  - `github.com/testcontainers/testcontainers-go` (for integration test DB containers)
- **Database & Schema:**
  - PostgreSQL database.
  - Atlas CLI (`atlas.hcl` & `schema.hcl`) for declarative schema definition and versioned SQL migration generation (`migrations/`).
  - `orcapostgres.NewMigrator(migrations.FS, nil)` for automatic startup migration execution.
- **Frontend Console:**
  - React 18 + TypeScript + Vite + Material UI (MUI - `@mui/material`, `@emotion/react`, `@emotion/styled`, `@mui/icons-material`)
  - Output built to `frontend/console/dist`
  - Embedded via Go `embed.FS` in package `frontend`
  - Served via `github.com/visdomtech/orcacommon/litespaserver`
- **TLS:** Environment-provided certificate/key paths or explicit `--allow-insecure` flag.

---

## Commands

```bash
# Backend build & test (integration tests automatically launch postgres testcontainer)
go build ./...                          # compile all Go packages
go test ./... -race -count=1            # run unit and integration tests with race detector
go vet ./...                            # run static analysis

# Schema management via Atlas
atlas migrate diff --env local          # generate migration script from schema.hcl changes
atlas migrate apply --env local         # apply migrations locally

# Console development & build
cd frontend/console && npm ci           # install frontend dependencies (React + Vite + MUI)
npm run dev                             # start Vite dev server
npm run build                           # compile SPA to dist/ for go:embed

# Full binary compilation (requires frontend/console/dist to exist)
cd frontend/console && npm run build && cd ../.. && go build ./cmd/ssetunnel

# Running CLI subcommands
./ssetunnel server --config server.yaml
./ssetunnel agent --server https://tunnel.example.com --token $AGENT_TOKEN --target 127.0.0.1:3000
./ssetunnel connect --server tunnel.example.com:9090 --token $USER_TOKEN --local 13306
```

---

## Project Structure (Deltas & Layout)

```
schema.hcl              → NEW: Declarative Atlas PostgreSQL database schema definition
atlas.hcl               → NEW: Atlas migration configuration file
migrations/             → NEW: SQL migration files managed by Atlas
  migrations.go         → NEW: Embeds migration files via //go:embed
cmd/ssetunnel/
  main.go               → MOD: add `connect` subcommand, auth & DB flags for `server`
frontend/               → NEW: Console frontend embedding package
  frontend.go           → Embeds console/dist via //go:embed
  console/              → React 18 + Vite + MUI console SPA
    package.json        → React 18, Vite, @mui/material, @emotion/react, @emotion/styled
    src/                → Components (Login, Tokens, Enrollment/PIN, Sessions)
    dist/               → Built SPA output
internal/auth/          → NEW: TOTP verification, PIN generator, bearer tokens, DB store
  store.go              → PostgreSQL token store via *pgxpool.Pool
  totp.go               → TOTP verification using pquerna/otp
  pin.go                → PIN generation (entropy, base32, single-use, 15m expiration)
  token.go              → Bearer token generation (crypto/rand hex) and validation
internal/consoleapi/    → NEW: JSON management API handlers
  login.go              → TOTP admin login & session cookie management
  tokens.go             → Token creation, listing, revocation endpoints
  agents.go             → Agent and active session status endpoints
internal/connect/       → NEW: Client wrapper
  client.go             → Local TCP listener, server dialer, token handshake injection
internal/server/        → MOD:
  middleware.go         → Auth middleware for /events, /up, /api
  consoleserver.go      → Console SPA route setup via orcacommon/litespaserver
  server.go             → Integration of orcapostgres pool, consoleapi, entry listener
```

---

## Code Style & Conventions

- **Database Access:** Always use `*pgxpool.Pool` obtained via `orcacommon/postgres` (`orcapostgres.OpenPool`). Never create custom pool initialization or raw driver connections manually.
- **Migrations:** Schema changes MUST be declared in `schema.hcl` and generated into `migrations/` via Atlas (`atlas migrate diff`). Automatic migration on startup is driven by `orcapostgres.NewMigrator(migrations.FS, nil)`.
- **Crypto & Security:**
  - Tokens generated with `crypto/rand` (minimum 32 bytes / 64 hex characters).
  - Constant-time comparison (`crypto/subtle.ConstantTimeCompare`) for token verification and session cookies.
  - Sensitive DB fields (PINs, session tokens) stored hashed or protected.
- **Embedded SPA Engine:** Use `github.com/visdomtech/orcacommon/litespaserver` to serve embedded console pages.
- **Errors:** Always wrap errors with descriptive context: `fmt.Errorf("auth: verify totp: %w", err)`.
- **Frontend Code:** React function components with TypeScript, MUI (`@mui/material`), React Router, TanStack Query, clean modular structure, zero default exports.

---

## Testing Strategy

1. **TestMain & Testcontainers (`internal/auth/`, `internal/server/`)**:
   - `TestMain` initializes the database pool via `orcapostgres.OpenPool` with `orcapostgres.DBConfig{ DatabaseURLTemplate: "postgres:tc:" }` to automatically spin up a PostgreSQL testcontainer and apply migrations via `orcapostgres.NewMigrator(migrations.FS, nil)`.
2. **Auth Core Unit Tests (`internal/auth/`)**:
   - `store_test.go`: PostgreSQL token store queries, concurrent token validation/revocation, transaction safety.
   - `pin_test.go`: Expiration after 15 min, single-use invalidation on second use, entropy checks.
   - `totp_test.go`: TOTP code verification with window clock skew tolerance.
   - `token_test.go`: Role checking (`agent`, `user`, `admin`), revocation status lookup.
3. **Console Management API & litespaserver Tests (`internal/consoleapi/`, `internal/server/`)**:
   - Admin TOTP login flow → HTTP-only cookie setting and validation.
   - PIN/Token issuance via management API endpoints.
   - Token revocation disabling subsequent API/tunnel authentication.
   - `litespaserver` correctly serves embedded SPA assets and falls back to `index.html` for unknown client-side routes.
4. **Connect Wrapper & End-to-End Tunnel Tests (`internal/connect/` & `internal/server/`)**:
   - `ssetunnel connect` local TCP acceptor dials entry listener with user bearer token.
   - User connection successfully connects to agent's local mock TCP server (e.g. echo server).
   - Unauthenticated user TCP connection is closed immediately by entry listener.
5. **Race Detector Gate**:
   - `go test ./... -race -count=1` must pass cleanly for all packages.

---

## Boundaries

- **Always:**
  - Execute `go test ./... -race -count=1` before marking tasks complete.
  - Use `orcacommon/postgres` for PostgreSQL connection management and migrations.
  - Use `testcontainers` (`postgres:tc:`) for local/integration testing.
  - Use `atlas` (`schema.hcl`, `atlas.hcl`) for database schema evolution.
  - Use `orcacommon/litespaserver` for serving the embedded React+Vite+MUI console SPA.
  - Fail closed on missing, expired, or invalid bearer tokens.
- **Ask First:**
  - Adding new Go or NPM dependencies beyond `orcacommon`, `pquerna/otp`, MUI, and standard React/Vite tooling.
  - Modifying `schema.hcl` tables without generating matching Atlas migrations.
- **Never:**
  - Store or log raw secret keys, PINs, or unhashed session cookies in plain text or logs.
  - Allow per-connection OTP (enrollment is OTP/PIN only; connections use bearer tokens).
  - Allow unauthenticated access to agent `/events`, `/up`, or management API routes.

---

## Success Criteria

1. **PostgreSQL & Atlas Integration**:
   - Database schema declared in `schema.hcl` and migrations embedded in `migrations/`.
   - `orcapostgres.OpenPool` successfully runs migrations on startup and manages connection pool.
   - Local/test environment spins up PostgreSQL testcontainer via `postgres:tc:` without manual DB setup.
2. **Enrollment & Token Issuance**:
   - `/register` POST endpoint validates TOTP or single-use PIN and returns a role-scoped bearer token (`agent` or `user`).
   - Single-use PIN expires after 15 minutes and cannot be reused.
3. **Auth Enforcement**:
   - Agent cannot connect to `/events` or POST to `/up` without a valid `agent` bearer token.
   - User cannot connect to entry listener without a valid `user` bearer token.
4. **Embedded Management Console (React + Vite + MUI + litespaserver)**:
   - Admin logs into web console using TOTP code.
   - Console UI built using React 18, Vite, and MUI (`@mui/material`).
   - Served seamlessly via `orcacommon/litespaserver`.
   - Console displays active agents, connected user sessions, total transferred bytes, and active tokens.
   - Console allows generating new single-use enrollment PINs, displaying TOTP setup QR codes, and revoking tokens.
5. **Connect Client Wrapper**:
   - `ssetunnel connect --server <host:port> --token <token> --local <port>` listens locally and forwards TCP traffic seamlessly to the agent target.
   - Works reliably with SSH `ProxyCommand` and raw TCP clients.
6. **Quality & Test Coverage**:
   - All unit and integration tests pass under `go test ./... -race -count=1`.

---

## Assumptions & Open Questions

- **Assumptions**:
  1. PostgreSQL instance connection parameters are supplied via environment variables or configuration file handled by `orcapostgres.DBConfig`.
  2. Single-use PINs are generated via the web console or server CLI flag (`--generate-pin`).
  3. The server entry listener handles incoming user TCP connections and reads a token handshake header/frame before bridging to yamux stream.
  4. Node.js/npm build environment is available during binary build process to produce `frontend/console/dist`.
- **Open Questions**: None blocking.
