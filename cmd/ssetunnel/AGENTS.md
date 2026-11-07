# CLI Entry Point

Multi-command CLI binary. Dispatches to server, agent, connect, login, or probe subcommands. Supports OS service management (start/stop/restart/status/reload/uninstall) via `kardianos/service`. Server and agent support `reload` (SIGHUP); connect does not.

## Commands

### `server`
```bash
ssetunnel server [--listen :8080] [--console-listen :8081] \
  [--heartbeat 15s] [--db-url URL] \
  [--metrics-dir ~/.ssetunnel/metrics] [--metrics-retention 7d] [--metrics-flush 10s] [--tuner-interval 30s] \
  [--disable-auth]
```
Runs the public tunnel server. Opens HTTP listener (agent tunnel + connect endpoints) and optional console HTTP listener. With `--disable-auth`, the DB pool and auth store are skipped — but **not** the console: it still listens on `:8081` whenever `--console-listen` is non-empty, and with a nil store `UserSessionMiddleware` injects a synthetic admin session, so the console and cloud shell are reachable with no credentials. Pass `--console-listen=""` to keep it off, and never combine `--disable-auth` with a reachable console port. Auto-seeds an admin user on first startup. All routes are root-relative; subpath prefixing is delegated to the reverse proxy. `--metrics-dir` defaults to `~/.ssetunnel/metrics` (pass `--metrics-dir=""` to disable). `--totp-secret` and `--base` are both accepted and ignored (deprecated); `--base` lingers only so installed OS services survive an in-place upgrade.

### `agent`
```bash
ssetunnel agent [--target 127.0.0.1:22] [--server <URL>] \
  [--id mydevbox] [--batch-size 262144] [--concurrency 1] [--compress] \
  [--no-auto-tune]
```
Runs the restricted-network agent. `--target` is optional (empty = dynamic target mode). `--server` is optional (default: from saved session). `--id` identifies the agent for server-side routing. `--no-auto-tune` disables server-driven parameter tuning (keeps static CLI flags). Uses session from `~/.ssetunnel/session` if available.

### `connect`
```bash
ssetunnel connect --local 127.0.0.1:3306 [--server <URL>] [--agent dev] [--target 127.0.0.1:22] [--batch-size 262144]
ssetunnel connect --local -  # Stdio mode for SSH ProxyCommand
```
User-side connection wrapper. Uses HTTP transport (SSE-down + POST-up) to connect to the server. `--server` is optional (default: from saved session). `--local -` enables stdio mode (stdin/stdout pipes for SSH ProxyCommand). `--agent` routes to a specific agent. `--target` enables dynamic target mode. `--batch-size` sets the upstream batch ceiling (0 = 256 KiB default; clamped to 1024..1048576).

Connect also supports service commands for running named daemon connections (see Service Management below). `--name` is mandatory for connect service commands to uniquely identify each connection instance.

### `login`
```bash
ssetunnel login [--server http://127.0.0.1:8081] [--tunnel-server <URL>]
```
Interactive username/password login with per-user TOTP and recovery code support. Checks TOTP enrollment via `/user-login-check` before prompting. `--server` is the console port URL for login API. `--tunnel-server` is the tunnel URL to save in session (default: derived from `--server` by replacing port 8081→8080). Derivation only works on a port swap: a console URL with no `:8081` — e.g. `https://tunnel.example.com` behind a proxy, where the tunnel lives at `/sse` — cannot be derived, so login prints which value it stored and says to pass `--tunnel-server`. It still stores, because a console and tunnel sharing one URL is a valid layout. Saves session token to `~/.ssetunnel/session`.

### `probe`
```bash
ssetunnel probe --server http://tunnel.example.com
```
Network diagnostics — outputs plain-text report with `--batch-size` / `--concurrency` recommendations.

## Service Management

Server and agent support full OS service actions via `kardianos/service`. Connect supports service actions with a mandatory `--name` flag for multi-instance support (no `reload`):

**Server / Agent:**
```bash
ssetunnel server start [--service-user ssetunnel]  # install + start as OS service
ssetunnel server stop                               # stop the service
ssetunnel server restart                            # restart the service
ssetunnel server status                             # check status
ssetunnel server reload                             # send SIGHUP for config reload
ssetunnel server run                                # run in foreground (service mode)
```

**Connect (named instances):**
```bash
ssetunnel connect start --name my-ssh --agent office --target 127.0.0.1:22 --local 127.0.0.1:2222
ssetunnel connect stop --name my-ssh                # stop the named connection
ssetunnel connect restart --name my-ssh             # restart the named connection
ssetunnel connect status --name my-ssh              # check status
ssetunnel connect run --name my-ssh [flags...]      # run in foreground (service mode)
```

Each named connect service gets a unique OS service name (`ssetunnel-connect-<name>`), PID file, and saved args file, so multiple connections can run simultaneously as daemons. Stdio mode (`--local -`) is not supported for service commands. `reload` is not supported for connect (no reloadable config).

Service user: non-root users get user-level services (systemd --user / LaunchAgents). Root requires `--service-user`. PID files are stored in `~/.ssetunnel/<name>.pid` for the reload action.

## Environment Variables
| Variable | Used by | Purpose |
|----------|---------|----------|
| `DATABASE_URL` | server | PostgreSQL connection URL |
| `SSETUNNEL_RECOVERY_CODE_PEPPER` | server | HMAC key for recovery code digests |

## Session File

Agent and connect commands load session tokens from `~/.ssetunnel/session` (JSON, multi-server), created by `ssetunnel login`. If `--server` is omitted, the first saved session is used (sorted alphabetically).

Session tokens are proactively refreshed when the remaining TTL falls below 7 days. Refresh uses `UpdateSessionToken` to preserve stored metadata (username, role). On refresh failure for expired tokens, a hard error directs the user to re-run `ssetunnel login`. Old-format sessions (without `expires_at`/`console_url`) log a warning but continue to work until server-side expiry.

## Rules
* All subcommands use `signal.NotifyContext` for graceful shutdown on SIGINT/SIGTERM.
* Agent flags are clamped: batch-size 1024..1048576, concurrency 1..4.
* Connect batch-size is clamped the same way; 0 means 256 KiB default.
* Git short SHA is embedded in the binary via ldflags and printed by `version` command.
* Embedded postgres cannot run as root (initdb restriction).
* SIGHUP handler is installed for server and agent (placeholder for config reload). Connect services do not support reload.
* Connect service commands require `--name` to uniquely identify each daemon instance (service name: `ssetunnel-connect-<name>`).
* Connect service commands reject stdio mode (`--local -`); use a local TCP address instead.
