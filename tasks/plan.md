# Implementation Plan - ssetunnel Cycle 3 (Auth, Management Console, Connect Wrapper)

## Architecture & Design

1. **Database & Schema Management (`schema.hcl`, `atlas.hcl`, `migrations/`)**:
   - Atlas declarative schema in `schema.hcl` (`tokens`, `pins`, `admin_sessions`).
   - Versioned SQL migrations in `migrations/` embedded via `//go:embed` (`migrations.FS`).
   - Startup auto-migrations using `orcapostgres.OpenPool` with `orcapostgres.NewMigrator(migrations.FS, nil)`.
   - Integration tests use `testcontainers` (`orcapostgres.DBConfig{ DatabaseURLTemplate: "postgres:tc:" }`).
   - Optional auth bypass if `--disable-auth` or no DB config is passed (100% backward compatibility for existing cycle 1/2 tests).

2. **Authentication Core & High Performance (`internal/auth/`)**:
   - Stores cryptographically secure SHA-256 digests (`crypto/rand` 32-byte tokens / base32 PINs).
   - In-memory `sync.Map` read-through token cache for sub-microsecond validation on high-frequency `/up` POST batch paths.
   - Atomic single-use PIN consumption via `UPDATE pins SET used_at = NOW() WHERE ... RETURNING role`.
   - TOTP validation via `pquerna/otp/totp`.

3. **High-Throughput Stream Proxying & Handshake (`internal/server/`, `internal/connect/`)**:
   - Line-delimited TCP handshake (`<token>\n` → `OK\n`) with 5s read deadline on user TCP entry connection.
   - Reusable `sync.Pool` (32KB buffers) with `io.CopyBuffer` to eliminate GC allocation churn during bidirectional stream proxying.
   - `ssetunnel connect` wrapper supports both **Local Port Mode** (`--local 13306`) and **Stdio Mode** (`--local -`) for SSH `ProxyCommand`.

4. **Embedded React Console & JSON Management API (`frontend/`, `internal/consoleapi/`)**:
   - React 18 + Vite + Material UI (`@mui/material`) in `frontend/console/`, built to `frontend/console/dist`, embedded in Go via `frontend/frontend.go`.
   - SPA catch-all routing powered by `github.com/visdomtech/orcacommon/litespaserver`.
   - Admin TOTP login yielding 12h HTTP-only session cookie.
   - JSON management API (`/api/v1/login`, `/api/v1/tokens`, `/api/v1/enroll`, `/api/v1/sessions`).
   - Live metrics (active sessions, throughput bytes) backed by Go in-memory `sync/atomic` counters on `Session` and `Registry` (zero DB write overhead).

## Rejected Alternatives

1. **File-based JSON Token Storage**: Rejected in favor of PostgreSQL + Atlas + `orcacommon/postgres` per user request and requirement to match `auth-go` standard.
2. **Direct DB queries on every `/up` POST batch**: Rejected because high-frequency `/up` batches (hundreds/sec) would saturate DB connection pools. Replaced with `sync.Map` in-memory read-through cache.
3. **Database-backed Live Traffic Metrics**: Rejected because updating DB rows on every proxied byte/frame creates severe write contention. Replaced with Go in-memory `sync/atomic` counters.
