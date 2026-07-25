# CLI Entry Point

Multi-command CLI binary. Dispatches to server, agent, connect, or probe subcommands.

## Commands

### `server`
```bash
ssetunnel server [--listen :8080] [--entry :9090] [--console-listen :8081] \
  [--heartbeat 15s] [--db-url URL] [--totp-secret SECRET] [--disable-auth]
```
Runs the public tunnel server. Opens HTTP, entry TCP, and optional console HTTP listeners. With `--disable-auth`, skips DB pool, auth store, and console server.

### `agent`
```bash
ssetunnel agent --target 127.0.0.1:22 [--server http://127.0.0.1:8080] \
  [--token TOKEN] [--batch-size 16384] [--concurrency 1] [--compress]
```
Runs the restricted-network agent. `--target` is required; `--server` defaults to `http://127.0.0.1:8080`.

### `connect`
```bash
ssetunnel connect --local 127.0.0.1:3306 [--server-entry 127.0.0.1:9090] [--token TOKEN]
ssetunnel connect --local -  # Stdio mode for SSH ProxyCommand
```
User-side connection wrapper. `--local -` enables stdio mode (stdin/stdout pipes for SSH ProxyCommand).

### `probe`
```bash
ssetunnel probe --server http://tunnel.example.com
```
Network diagnostics — outputs plain-text report with `--batch-size` / `--concurrency` recommendations.

## Environment Variables
| Variable | Used by | Purpose |
|----------|---------|---------|
| `DATABASE_URL` | server | PostgreSQL connection URL |
| `SSETUNNEL_TOTP_SECRET` | server | Admin TOTP secret |
| `SSETUNNEL_TOKEN` | agent, connect | Bearer token (flag overrides) |

## Rules
* All subcommands use `signal.NotifyContext` for graceful shutdown on SIGINT/SIGTERM.
* Agent flags are clamped: batch-size 1024..1048576, concurrency 1..4.
