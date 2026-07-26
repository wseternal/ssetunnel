# ssetunnel

`ssetunnel` exposes private TCP services behind strict corporate firewalls through a public server over an **SSE-down + Batched-POST-up** HTTP transport. It is specifically designed for agents stuck behind restrictive outbound HTTP/HTTPS proxies where traditional SSH or direct TCP connections are blocked.

---

## Overview & How It Works

```
┌─────────────────┐        TCP Handshake / Proxy       ┌─────────────────┐
│ User / App      ├───────────────────────────────────►│ ssetunnel       │
└─────────────────┘                                    │ connect         │
                                                       └────────┬────────┘
                                                                │ TCP + Bearer Token
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

1. **`ssetunnel server`**: Serves as the public relay point. Receives SSE-down connections and batched HTTP POST requests from agents, provides TCP entry listeners for users, manages PostgreSQL token authentication, and hosts an embedded React Admin Management Console SPA.
2. **`ssetunnel agent`**: Runs inside the restricted network. Connects outbound to the public server via SSE + HTTP POST, negotiates stream window parameters (`yamux`), and proxies TCP traffic to local target services.
3. **`ssetunnel connect`**: Client wrapper for users/CLI applications. Exposes a local TCP port or Stdio interface (`--local -`), performs token handshake with the server's TCP entry listener, and transparently forwards TCP streams.
4. **`ssetunnel probe`**: Diagnostic tool to measure path capabilities (POST body ceilings, response latency, throttling) between restricted network environments and the server.

> **Multi-user multiplexing**: A single agent tunnel supports multiple concurrent user connections simultaneously. Each user gets an independent yamux stream over the shared agent tunnel, and the agent opens a separate TCP connection to the target service for each stream. No additional agents or tunnels are needed — just point more users at the same entry listener.

---

## Quick Start

### 1. Build from Source

```bash
# Build embedded React frontend
cd frontend/console
npm install
npm run build
cd ../..

# Build ssetunnel binary
go build -o ssetunnel ./cmd/ssetunnel
```

### 2. Basic Local Demonstration

**Start Server** (dev mode, authentication disabled):
```bash
./ssetunnel server --listen :8080 --entry :9090 --console-listen :8081 --disable-auth
```

**Run Agent** (forwarding to a local web server at `127.0.0.1:3000`):
```bash
./ssetunnel agent --server http://localhost:8080 --target 127.0.0.1:3000
```

**Connect Client Wrapper**:
```bash
./ssetunnel connect --server-entry 127.0.0.1:9090 --local 127.0.0.1:8000
```

Now accessing `http://127.0.0.1:8000` routes directly to the private target at `127.0.0.1:3000`!

> **With authentication enabled** (default), start the server without `--disable-auth`, note the auto-generated admin password from the log output, then run `ssetunnel login` to store a session token before starting the agent and connect.

---

## CLI Command Usage

### `ssetunnel server`

Runs the public tunnel relay server, TCP entry listener, and embedded Admin Management Console.

```bash
ssetunnel server [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--listen` | `:8080` | HTTP listen address for agent tunnel endpoints (`/events`, `/up`) |
| `--entry` | `:9090` | TCP entry listen address for user connections |
| `--console-listen` | `:8081` | HTTP listen address for embedded Admin Console SPA & Management API |
| `--heartbeat` | `15s` | SSE heartbeat interval |
| `--db-url` | `DATABASE_URL` or `postgres:tc:` | PostgreSQL database connection string (defaults to TestContainer if empty) |
| `--totp-secret` | `SSETUNNEL_TOTP_SECRET` | Base32 TOTP secret for admin console login |
| `--disable-auth` | `false` | Disable authentication enforcement (backward compatibility mode) |

---

### `ssetunnel agent`

Runs inside the restricted network to forward traffic from the server to local TCP services.

```bash
ssetunnel agent [--server <URL>] [--target <TCP_ADDR>] [--id <AGENT_ID>] [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `http://127.0.0.1:8080` | Tunnel server base URL |
| `--target` | *(empty)* | Target TCP address (empty = dynamic target mode, reads from stream header) |
| `--id` | *(empty)* | Agent identifier for server-side routing |
| `--batch-size` | `16384` | Upstream POST batch ceiling in bytes (`1024` to `1048576`) |
| `--concurrency` | `1` | Upstream POST parallel sender depth (`1` to `4`) |
| `--compress` | `false` | Negotiate gzip compression per batch |

Uses session token from `~/.ssetunnel/session` (created by `ssetunnel login`) if available.

---

### `ssetunnel connect`

User-side client wrapper that injects authentication token handshakes and bridges local TCP/Stdio to the server entry listener.

```bash
ssetunnel connect --server-entry <TCP_ADDR> --local <LISTEN_ADDR> [--agent <ID>] [--target <ADDR>] [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--server-entry` | `127.0.0.1:9090` | Tunnel server entry TCP address |
| `--agent` | *(empty)* | Agent identifier to route to (for multi-agent setups) |
| `--target` | *(empty)* | Target address on the agent (for dynamic target mode) |
| `--local` | *(Required)* | Local TCP listen address (e.g. `127.0.0.1:3306`) or `-` for Stdio mode |

Uses session token from `~/.ssetunnel/session` (created by `ssetunnel login`) if available.

#### Stdio Mode Example (SSH ProxyCommand / Git Integration)
```bash
# In ~/.ssh/config:
Host private-server
    ProxyCommand ssetunnel connect --server-entry tunnel.example.com:9090 --agent dev --target 127.0.0.1:22 --local -
```

---

### `ssetunnel login`

Interactive username/password authentication with optional TOTP. Saves session token for agent and connect commands.

```bash
ssetunnel login [--console <URL>]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--console` | `http://127.0.0.1:8081` | Console API URL |

Saves session token to `~/.ssetunnel/session`.

---

### `ssetunnel probe`

Measures HTTP POST path limits and latency characteristics between the agent environment and the server.

```bash
ssetunnel probe --server http://localhost:8080
```

---

### `ssetunnel version`

Prints the build version and embedded git short SHA.

```bash
ssetunnel version
```

---

## Local Development & Testing

### Prerequisites

- **Go**: 1.26.5 or newer
- **Node.js**: 18+ and `npm` (for frontend console build)
- **Docker**: Required if using the default `postgres:tc:` local TestContainers database
- **Atlas CLI** *(Optional)*: For schema management (`atlas.hcl`)

### Project Structure

```
├── cmd/ssetunnel/          # Main CLI application entry point
├── docs/                   # Specifications, ideas, and architecture documents
├── frontend/               # React 18 + Vite + MUI console SPA source and embed.FS
├── internal/
│   ├── agent/              # Agent connection lifecycle and reconnect logic
│   ├── auth/               # Token, TOTP, single-use PIN and PostgreSQL storage engine
│   ├── connect/            # Client wrapper for Local Port and Stdio modes
│   ├── consoleapi/         # Management JSON API handlers
│   ├── consoleserver/      # Consolidated SPA + API HTTP server
│   ├── mux/                # Yamux session wrapper
│   ├── probe/              # Network path capability probe
│   ├── server/             # Server session registry and TCP entry listener
│   └── transport/          # SSE-down and batched-POST-up transport
├── migrations/             # Atlas-generated SQL migration files
├── schema.hcl              # Atlas PostgreSQL declarative schema definition
└── tasks/                  # Implementation checklists
```

### Running Tests

Run all unit and end-to-end integration tests with race detection:

```bash
go test ./... -race -count=1
```

*Note: Tests automatically spawn an isolated PostgreSQL instance using TestContainers if `postgres:tc:` is specified.*

### Frontend Development

To run the Admin Console React frontend in development mode with hot reload:

```bash
cd frontend/console
npm install
npm run dev
```

To build and embed the production bundle into Go code:

```bash
cd frontend/console
npm run build
```

### Database Schema Management

Schema changes are managed using Atlas. To validate or apply schema definitions:

```bash
# Validate schema against migration directory
atlas migrate validate --env local
```

---

## Agent Routing and Dynamic Target

When multiple agents are registered, use `--agent <id>` to route to a specific agent:

```bash
# Agent identifies itself
./ssetunnel agent --server http://localhost:8080 --id devbox

# User connects to specific agent
./ssetunnel connect --server-entry 127.0.0.1:9090 --agent devbox --target 127.0.0.1:22 --local -
```

**Dynamic target mode**: When the agent runs without `--target`, it reads the target address from each stream's header. This allows one agent to proxy to multiple services:

```bash
# Agent in dynamic target mode
./ssetunnel agent --server http://localhost:8080 --id devbox

# Connect to SSH
./ssetunnel connect --agent devbox --target 127.0.0.1:22 --local -

# Connect to database
./ssetunnel connect --agent devbox --target 127.0.0.1:5432 --local 127.0.0.1:15432
```

Agent configs in the console define allowed targets per agent (e.g., `127.0.0.1:*` allows any port on localhost).

---

## License

`ssetunnel` is released under the Apache 2.0 License.
