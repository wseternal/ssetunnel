# Migrations

Embedded Atlas SQL migration files for PostgreSQL schema management.

## Structure

```
migrations/
├── 20260724000001_initial_schema.sql   # Tables: tokens, pins, admin_sessions
├── atlas.sum                            # Atlas migration checksum
├── migrations.go                        # go:embed FS export
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
Bearer tokens for agent and user authentication. Stores SHA-256 digest, role, description, timestamps (created, expires, revoked).

### `pins`
Single-use PINs for agent enrollment. Stores digest, role, expiry, used_at timestamp.

### `admin_sessions`
Cookie-based admin sessions. Stores digest and expiry.

## Rules
* New migrations: create `YYYYMMDDHHMMSS_description.sql` and update `atlas.sum`.
* Never modify existing migrations — always add new ones.
