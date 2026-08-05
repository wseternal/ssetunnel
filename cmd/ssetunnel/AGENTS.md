# CLI Entry Point

Multi-command CLI binary. Dispatches to server, agent, connect, login, or probe subcommands. Supports OS service management (start/stop/restart/status/reload) via `kardianos/service`.

## Commands

### `server`
```bash
ssetunnel server [--listen :8080] [--console-listen :8081] \
  [--base /tunnel] [--heartbeat 15s] [--db-url URL] \
  [--metrics-dir ~/.ssetunnel/metrics] [--metrics-retention 7d] [--metrics-flush 10s] [--tuner-interval 30s] \
  [--disable-auth]
```
Runs the public tunnel server. Opens HTTP listener (agent tunnel + connect endpoints) and optional console HTTP listener. With `--disable-auth`, skips DB pool, auth store, and console server. Auto-seeds an admin user on first startup. `--base` prefixes all tunnel endpoints (e.g. `/tunnel/events`). `--metrics-dir` defaults to `~/.ssetunnel/metrics` (pass `--metrics-dir=""` to disable). `--totp-secret` is deprecated (per-user TOTP is now used).

### `agent`
```bash
ssetunnel agent [--target 127.0.0.1:22] [--server <URL>] \
  [--base /tunnel] [--id mydevbox] [--batch-size 262144] [--concurrency 1] [--compress] \
  [--no-auto-tune]
```
Runs the restricted-network agent. `--target` is optional (empty = dynamic target mode). `--server` is optional (default: from saved session). `--id` identifies the agent for server-side routing. `--base` must match the server's `--base`. `--no-auto-tune` disables server-driven parameter tuning (keeps static CLI flags). Uses session from `~/.ssetunnel/session` if available.

### `connect`
```bash
ssetunnel connect --local 127.0.0.1:3306 [--server <URL>] [--base /tunnel] [--agent dev] [--target 127.0.0.1:22] [--batch-size 262144]
ssetunnel connect --local -  # Stdio mode for SSH ProxyCommand
```
User-side connection wrapper. Uses HTTP transport (SSE-down + POST-up) to connect to the server. `--server` is optional (default: from saved session). `--local -` enables stdio mode (stdin/stdout pipes for SSH ProxyCommand). `--agent` routes to a specific agent. `--target` enables dynamic target mode. `--batch-size` sets the upstream batch ceiling (0 = 256 KiB default; clamped to 1024..1048576).

### `login`
```bash
ssetunnel login [--server http://127.0.0.1:8081] [--tunnel-server <URL>]
```
Interactive username/password login with per-user TOTP and recovery code support. Checks TOTP enrollment via `/user-login-check` before prompting. `--server` is the console port URL for login API. `--tunnel-server` is the tunnel URL to save in session (default: derived from `--server` by replacing port 8081→8080). Saves session token to `~/.ssetunnel/session`.

### `probe`
```bash
ssetunnel probe --server http://tunnel.example.com [--base /tunnel]
```
Network diagnostics — outputs plain-text report with `--batch-size` / `--concurrency` recommendations.

## Service Management

Server and agent support OS service actions via `kardianos/service`:
```bash
ssetunnel server start [--service-user ssetunnel]  # install + start as OS service
ssetunnel server stop                               # stop the service
ssetunnel server restart                            # restart the service
ssetunnel server status                             # check status
ssetunnel server reload                             # send SIGHUP for config reload
ssetunnel server run                                # run in foreground (service mode)
```

Service user: non-root users get user-level services (systemd --user / LaunchAgents). Root requires `--service-user`. PID files are stored in `~/.ssetunnel/<name>.pid` for the reload action.

## Environment Variables
| Variable | Used by | Purpose |
|----------|---------|----------|
| `DATABASE_URL` | server | PostgreSQL connection URL |
| `SSETUNNEL_RECOVERY_CODE_PEPPER` | server | HMAC key for recovery code digests |

## Session File

Agent and connect commands load session tokens from `~/.ssetunnel/session` (JSON, multi-server), created by `ssetunnel login`. If `--server` is omitted, the first saved session is used (sorted alphabetically).

## Rules
* All subcommands use `signal.NotifyContext` for graceful shutdown on SIGINT/SIGTERM.
* Agent flags are clamped: batch-size 1024..1048576, concurrency 1..4.
* Connect batch-size is clamped the same way; 0 means 256 KiB default.
* Git short SHA is embedded in the binary via ldflags and printed by `version` command.
* `--base` must start with `/` (validated by `validateBasePath`).
* Embedded postgres cannot run as root (initdb restriction).
* SIGHUP handler is installed for server and agent (placeholder for config reload).
