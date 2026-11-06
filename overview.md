# ssetunnel

`ssetunnel` exposes private TCP services behind strict corporate firewalls through a public server over an **SSE-down + Batched-POST-up** HTTP transport. It is specifically designed for agents stuck behind restrictive outbound HTTP/HTTPS proxies where traditional SSH or direct TCP connections are blocked.

## Architecture

```
┌─────────────────┐        TCP Handshake / Proxy       ┌─────────────────┐
│ User / App      ├───────────────────────────────────►│ ssetunnel       │
└─────────────────┘                                    │ connect         │
                                                       └────────┬────────┘
                                                                │ HTTP (SSE-Down + POST-Up)
                                                                ▼
┌─────────────────┐      Admin Console SPA (MUI)       ┌─────────────────┐
│ Web Browser     ├───────────────────────────────────►│ ssetunnel       │
└─────────────────┘                                    │ server          │
                                                       └────────▲────────┘
                                                                │ SSE-Down + Batched-POST-Up
                                                                │ (Yamux Multiplexing)
                                                       ┌────────┴────────┐
┌─────────────────┐        Forward Stream TCP          │ ssetunnel       │
│ Private Service ◄────────────────────────────────────┤ agent           │
└─────────────────┘                                    └─────────────────┘
```

---

## Quick Start (Local, No Auth)

The simplest way to try ssetunnel on localhost. No authentication, no database setup.

**Terminal 1 — Server:**
```bash
ssetunnel server run --disable-auth
```

**Terminal 2 — Agent** (forwarding to local sshd at `127.0.0.1:22`):
```bash
ssetunnel agent run --server http://127.0.0.1:8080 --target 127.0.0.1:22 --id mybox
```

**Terminal 3 — Connect (SSH via ProxyCommand):**
```bash
ssh -o ProxyCommand="ssetunnel connect --server http://127.0.0.1:8080 --agent mybox --target 127.0.0.1:22 --local -" user@127.0.0.1
```

**Terminal 3 — Connect (VNC via local TCP port):**
```bash
ssetunnel connect --server http://127.0.0.1:8080 --agent mybox --target 127.0.0.1:5900 --local 127.0.0.1:5900
```
Then use a VNC client to connect to `127.0.0.1:5900`.

---

## Quick Start (Local, With Auth)

Same as above but with token authentication enabled (the default). The server uses an embedded PostgreSQL database.

**Terminal 1 — Server:**
```bash
ssetunnel server run
```
Note the auto-generated admin password from the log output.

**Terminal 2 — Login, then Agent:**
```bash
ssetunnel login --server http://127.0.0.1:8081
ssetunnel agent run --server http://127.0.0.1:8080 --target 127.0.0.1:22 --id mybox
```

**Terminal 3 — Login, then Connect (SSH):**
```bash
ssetunnel login --server http://127.0.0.1:8081
ssh -o ProxyCommand="ssetunnel connect --server http://127.0.0.1:8080 --agent mybox --target 127.0.0.1:22 --local -" user@127.0.0.1
```

---

## Production Setup (Behind Caddy, Multi-Machine)

A typical deployment: server behind Caddy reverse proxy on a public VPS, agent on a private machine, connect from anywhere.

### Server (public VPS)

Start the server as a background service with embedded PostgreSQL:

```bash
ssetunnel server start --db-url "postgres:embedded:?datapath=$HOME/.ssetunnel/data"
```

Configure Caddy to route traffic. The admin console is served at the proxy **root** and
only the tunnel endpoints carry a prefix, which the reverse proxy strips so the server
sees root-relative routes:

```caddyfile
https://tunnel.example.com {
	handle_path /sse* {
		reverse_proxy http://127.0.0.1:8080
	}
	handle /assets/* {
		encode zstd gzip
		reverse_proxy http://127.0.0.1:8081
	}
	handle {
		reverse_proxy http://127.0.0.1:8081
	}
}
```

Keep the braces on their own lines — a single-line `handle_path /sse* { reverse_proxy ... }`
does not parse in Caddy v2. `handle_path` matches first, so `/sse/*` reaches the tunnel
server with the prefix stripped, then `/assets/*`, then the bare `handle` takes the rest of
the console (`/`, `/api/v1/*`).

Limit `encode` to the `/assets/*` block. Scoping it to the console port is not enough: the
cloud shell streams over `/api/v1/shell/connect` on that same port, and Caddy's default
matcher compresses any `text/*`, which gzip-buffers the live terminal (12% more bytes with
per-frame flushing). The bundles are what benefit — 1,304,069 bytes raw against 393,664
gzipped. SSE responses also carry `no-transform`, so a stray site-level `encode` cannot
capture them.

Open the admin console at `https://tunnel.example.com`, log in with the admin user, and create a user/token for the agent.

### Agent (private machine)

Log in once to obtain and save a session token. `--tunnel-server` is required because the
login URL is the console root and only a `:8081`→`:8080` port swap is derived
automatically — a different path prefix is not:

```bash
ssetunnel login --server https://tunnel.example.com --tunnel-server https://tunnel.example.com/sse
```

Start the agent as a background service:

```bash
ssetunnel agent start --id office
```

### Client (any machine)

Log in once (same `--tunnel-server` reasoning as above):

```bash
ssetunnel login --server https://tunnel.example.com --tunnel-server https://tunnel.example.com/sse
```

**SSH via ProxyCommand:**
```bash
ssh -o ProxyCommand="ssetunnel connect --server https://tunnel.example.com/sse --agent office --target 127.0.0.1:22 --local -" user@127.0.0.1
```

**VNC via local TCP port:**
```bash
ssetunnel connect --server https://tunnel.example.com/sse --agent office --target 127.0.0.1:5900 --local 127.0.0.1:5900
```
Then connect your VNC client to `127.0.0.1:5900`.

**Named daemon connections** (run multiple connections simultaneously as background services):
```bash
# Start a persistent SSH connection daemon named "office-ssh"
ssetunnel connect start --name office-ssh --agent office --target 127.0.0.1:22 --local 127.0.0.1:2222

# Start a persistent DB connection daemon named "office-db"
ssetunnel connect start --name office-db --agent office --target 127.0.0.1:5432 --local 127.0.0.1:5432

# Manage individual connections
ssetunnel connect status --name office-ssh
ssetunnel connect stop --name office-ssh
```
Each `--name` creates a unique OS service (`ssetunnel-connect-<name>`) so multiple connections coexist without conflict.

---

## Service Management

The `start`, `stop`, `restart`, `status`, and `reload` actions manage the service as an OS daemon (systemd on Linux, launchd on macOS):

```bash
# Start as a background service (installs + starts)
ssetunnel server start [flags...]

# Stop, restart, check status
ssetunnel server stop
ssetunnel server restart
ssetunnel server status

# Reload configuration via SIGHUP
ssetunnel server reload

# Run in foreground (no daemonization)
ssetunnel server run [flags...]
```

Re-running `start` with different flags refreshes the service definition automatically.

### Connect Service Commands

The `connect` sub-command also supports service actions for running named daemon connections. The `--name` flag is **mandatory** and uniquely identifies each connection instance:

```bash
# Start a named connection daemon (e.g. SSH tunnel to office agent)
ssetunnel connect start --name office-ssh --agent office --target 127.0.0.1:22 --local 127.0.0.1:2222

# Start another named connection (e.g. DB tunnel)
ssetunnel connect start --name office-db --agent office --target 127.0.0.1:5432 --local 127.0.0.1:5432

# Manage individual connections
ssetunnel connect stop --name office-ssh
ssetunnel connect restart --name office-ssh
ssetunnel connect status --name office-ssh

# Uninstall (stops + removes service definition + cleans up)
ssetunnel connect uninstall --name office-ssh
```

Each named connect creates a unique OS service (`ssetunnel-connect-<name>`), so multiple connections can run simultaneously as background daemons. Note: `reload` is not supported for connect (no reloadable config), and stdio mode (`--local -`) is not available in service mode.

---

## License

`ssetunnel` is released under the MIT License.
