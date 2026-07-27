# CLI Entry Point

Multi-command CLI binary. Dispatches to server, agent, connect, login, or probe subcommands.

## Commands

### `server`
```bash
ssetunnel server [--listen :8080] [--console-listen :8081] \
  [--heartbeat 15s] [--db-url URL] [--totp-secret SECRET] [--disable-auth]
```
Runs the public tunnel server. Opens HTTP listener (agent tunnel + connect endpoints) and optional console HTTP listener. With `--disable-auth`, skips DB pool, auth store, and console server. Auto-seeds an admin user on first startup.

### `agent`
```bash
ssetunnel agent [--target 127.0.0.1:22] [--server http://127.0.0.1:8080] \
  [--id mydevbox] [--batch-size 16384] [--concurrency 1] [--compress]
```
Runs the restricted-network agent. `--target` is optional (empty = dynamic target mode). `--id` identifies the agent for server-side routing. Uses session from `~/.ssetunnel/session` if available.

### `connect`
```bash
ssetunnel connect --local 127.0.0.1:3306 [--server http://127.0.0.1:8080] [--agent dev] [--target 127.0.0.1:22]
ssetunnel connect --local -  # Stdio mode for SSH ProxyCommand
```
User-side connection wrapper. Uses HTTP transport (SSE-down + POST-up) to connect to the server. `--local -` enables stdio mode (stdin/stdout pipes for SSH ProxyCommand). `--agent` routes to a specific agent. `--target` enables dynamic target mode.

### `login`
```bash
ssetunnel login [--console http://127.0.0.1:8081]
```
Interactive username/password login with optional TOTP. Saves session token to `~/.ssetunnel/session`.

### `probe`
```bash
ssetunnel probe --server http://tunnel.example.com
```
Network diagnostics — outputs plain-text report with `--batch-size` / `--concurrency` recommendations.

## Environment Variables
| Variable | Used by | Purpose |
|----------|---------|----------|
| `DATABASE_URL` | server | PostgreSQL connection URL |
| `SSETUNNEL_TOTP_SECRET` | server | Admin TOTP secret |

## Session File

Agent and connect commands load session tokens from `~/.ssetunnel/session`, created by `ssetunnel login`.

## Rules
* All subcommands use `signal.NotifyContext` for graceful shutdown on SIGINT/SIGTERM.
* Agent flags are clamped: batch-size 1024..1048576, concurrency 1..4.
* Git short SHA is embedded in the binary via ldflags and printed by `version` command.
