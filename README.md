# ssetunnel

**Expose private TCP services through a public server — over HTTP.**

SSH into machines behind corporate firewalls. Forward VNC, databases, any TCP port. No inbound rules, no VPN, no port forwarding. Just outbound HTTP.

```
ssh -o ProxyCommand="ssetunnel connect --server https://www.yourserver.com --agent mm --target 127.0.0.1:22 --local -" your_ssh_user@127.0.0.1
```

That's it. One command. You're in.

---

## How It Works

Your **agent** (on the private machine) dials *out* to the public server over plain HTTP. Your **client** dials *in* from anywhere. The server multiplexes them together using yamux over an SSE-down + batched-POST-up transport.

Firewalls see nothing but outbound HTTPS. No inbound ports. No TCP tunnels on the wire.

```
You (SSH, DB client, VNC …)
  │
  ▼
┌──────────────┐       HTTP (SSE + POST)       ┌──────────────┐       TCP / PTY       ┌──────────────┐
│   connect    │ ◄══════════════════════════►  │    server    │ ◄══════════════════►  │    agent     │
└──────────────┘        public internet        └──────────────┘    private network    └──────────────┘
                                                                                             │
                                                                                             ▼
                                                                                       sshd / DB / VNC
```

> For the full architecture deep-dive, service management, and production deployment guide, see [overview.md](overview.md).

---

## In Action

**Live tunnel sessions** — monitor every connected agent, bandwidth, and connection in real time from the web console.

![Console Sessions](docs/assets/console-sessions.png)

**Cloud Shell** — jump straight into a terminal on any connected agent from your browser. No SSH client needed.

![Cloud Shell](docs/assets/console-shell.png)

**Remote Desktop** — view remote desktop from any connected agent from your browser. No remote desktop client needed.
![Remote Desktop](docs/assets/remote-desktop.png)

**Your agent, your network** — the agent runs on the private machine and forwards traffic to local services (sshd, VNC, databases, anything listening on TCP).

---

## Quick Start

### 1. Start the server (local dev, no auth)

```bash
ssetunnel server run --disable-auth
```

### 2. Run an agent on the private machine

```bash
ssetunnel agent run --server http://127.0.0.1:8080 --target 127.0.0.1:22 --id mybox
```

### 3. SSH through the tunnel

```bash
ssh -o ProxyCommand="ssetunnel connect --server http://127.0.0.1:8080 --agent mybox --target 127.0.0.1:22 --local -" user@127.0.0.1
```

Or forward any TCP port (e.g. VNC on `:5900`):

```bash
ssetunnel connect --server http://127.0.0.1:8080 --agent mybox --target 127.0.0.1:5900 --local 127.0.0.1:5900
```

---

## Production (Behind a Reverse Proxy)

Deploy the server on a public VPS behind Caddy. The admin console is served at the
proxy **root**; only the tunnel endpoints sit under a prefix, which the proxy strips so
the server sees root-relative routes:

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

Keep the braces on their own lines: `handle_path /sse* { reverse_proxy ... }` written on a
single line does not parse (Caddy v2.11 rejects it with "Unexpected next token after '{'
on same line"). `handle_path` is matched first, so `/sse/*` reaches the tunnel server with
the prefix stripped, then `/assets/*`, then the bare `handle` catches the rest of the console.

Put `encode` in the `/assets/*` block and nowhere else. Scoping it to the console port is
**not** enough: the cloud shell streams over `/api/v1/shell/connect` on that same port, and
Caddy's default matcher compresses any `text/*`, so it gzip-buffers the live terminal —
measured at 12% more bytes with per-frame flushing. Only the hashed bundles want
compression: the JS is 1,304,069 bytes raw, 393,664 gzipped (385,845 with zstd). The server
also marks SSE responses `no-transform`, so a stray site-level `encode` cannot capture them.

Start as a managed OS service (systemd / launchd):

```bash
# Server (public VPS)
ssetunnel server start --db-url "postgres:embedded:?datapath=$HOME/.ssetunnel/data"

# Agent (private machine) — login once, then start.
# --tunnel-server is required here: the login URL is the console root, and only
# a :8081→:8080 port swap is derived automatically, not a different path prefix.
ssetunnel login --server https://tunnel.example.com --tunnel-server https://tunnel.example.com/sse
ssetunnel agent start --id office

# Client (anywhere) — SSH in
ssetunnel login --server https://tunnel.example.com --tunnel-server https://tunnel.example.com/sse
ssh -o ProxyCommand="ssetunnel connect --server https://tunnel.example.com/sse --agent office --target 127.0.0.1:22 --local -" user@127.0.0.1
```

---

## What Can You Tunnel?

| Use Case | Command |
|----------|---------|
| **SSH** | `ssh -o ProxyCommand="ssetunnel connect --agent mybox --target 127.0.0.1:22 --local -" user@host` |
| **VNC / RDP** | `ssetunnel connect --agent mybox --target 127.0.0.1:5900 --local 127.0.0.1:5900` |
| **Database** | `ssetunnel connect --agent mybox --target 127.0.0.1:5432 --local 127.0.0.1:15432` |
| **Any TCP** | `ssetunnel connect --agent mybox --target <host>:<port> --local <host>:<port>` |

---

## Further Reading

- [Full Architecture & Deployment Guide](overview.md) — detailed setup, service management, auth modes, and production tips
- [CHANGELOG.md](CHANGELOG.md) — release notes

---

## License

`ssetunnel` is released under the MIT License.
