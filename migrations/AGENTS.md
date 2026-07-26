# Migrations

Embedded Atlas SQL migration files for PostgreSQL schema management.

## Structure

```
migrations/
├── 20260724000001_initial_schema.sql    # Tables: tokens, pins, admin_sessions
├── 20260724000002_add_users.sql         # Table: users
├── 20260724000003_add_user_id_fk.sql    # FK: tokens.user_id -> users.id
├── 20260724000004_add_user_sessions.sql # Table: user_sessions
├── 20260724000005_cleanup_legacy.sql    # Drop legacy columns
├── 20260724000006_add_user_permissions.sql # Columns: can_connect, can_create_agent
├── 20260724000007_add_agents.sql        # Table: agents (with allowed_targets)
├── atlas.sum                             # Atlas migration checksum
├── migrations.go                         # go:embed FS export
└── migrations_test.go
```

## Usage

```go
import "github.com/wseternal/ssetunnel/migrations"

// migrations.FS is an embed.FS containing all .sql files + atlas.sum
pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
```

The migration runner logic lives in `github.com/visdomtech/orcacommon/postgres`. This package is a thin data-only wrapper.

## Schema

### `tokens`
Bearer tokens for agent and user authentication. Stores SHA-256 digest, role, description, timestamps (created, expires, revoked), and user_id FK.

### `pins`
Single-use PINs for agent enrollment. Stores digest, role, expiry, used_at timestamp.

### `admin_sessions`
Cookie-based admin sessions. Stores digest and expiry.

### `users`
User accounts with username, password hash, role (admin/user), and permission flags:
- `can_connect`: Allow user to connect to tunnel
- `can_create_agent`: Allow user to register agents

### `user_sessions`
User login sessions with digest, user_id FK, and expiry. Supports immediate revocation via DELETE.

### `agents`
Agent routing configurations:
- `agent_id` (nullable): Unique agent identifier. NULL row serves as default config.
- `allowed_targets` (text[]): Target address patterns (e.g., `127.0.0.1:*`, `*`)
- `description`: Human-readable description

## Rules
* New migrations: create `YYYYMMDDHHMMSS_description.sql` and update `atlas.sum`.
* Never modify existing migrations — always add new ones.
* Use `text[]` for PostgreSQL arrays (pgx scans natively to `[]string`).
