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
ssetunnel server start --base /sse --db-url "postgres:embedded:?datapath=$HOME/.ssetunnel/data"
```

Configure Caddy to route traffic:

```caddyfile
https://tunnel.example.com {
    reverse_proxy /sse*     http://127.0.0.1:8080
    reverse_proxy /console* http://127.0.0.1:8081
    file_server
}
```

Open the admin console at `https://tunnel.example.com/console`, log in with the admin user, and create a user/token for the agent.

### Agent (private machine)

Log in once to obtain and save a session token:

```bash
ssetunnel login --server https://tunnel.example.com
```

Start the agent as a background service:

```bash
ssetunnel agent start --id office --base /sse
```

### Client (any machine)

Log in once:

```bash
ssetunnel login --server https://tunnel.example.com
```

**SSH via ProxyCommand:**
```bash
ssh -o ProxyCommand="ssetunnel connect --base /sse --server https://tunnel.example.com --agent office --target 127.0.0.1:22 --local -" user@127.0.0.1
```

**VNC via local TCP port:**
```bash
ssetunnel connect --base /sse --server https://tunnel.example.com --agent office --target 127.0.0.1:5900 --local 127.0.0.1:5900
```
Then connect your VNC client to `127.0.0.1:5900`.

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

---

## License

`ssetunnel` is released under the MIT License.
