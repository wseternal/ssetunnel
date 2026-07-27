# Connect Client

The connect client is the user-side component that bridges local applications (SSH clients, DB tools) to the tunnel server via HTTP transport (SSE-down + POST-up, same as agents).

## Architecture

```
SSH ProxyCommand (--local -)          Local Port Mode (--local 127.0.0.1:3306)
         │                                      │
   ServeStdio/ServeRW                    ServeListener
         │                                      │
   Dial (HTTP transport)                  Dial (per connection)
         │                                      │
   io.Copy (stdin↔server)                io.Copy (localConn↔server)
```

## Core Types

### `Client`
Holds `serverURL`, `token`, `agentID` (for routing), and `target` (for dynamic target mode). Created via `NewClient(url, token, agentID, target)`.

### `ServeRW(ctx, r, w)`
The critical function for SSH ProxyCommand support. Copies bidirectionally between `r`/`w` (typically stdin/stdout pipes) and the server connection.

**Error handling**: On connection failure, writes the error to both `os.Stderr` and `w` (stdout) so the SSH client displays the actual error before its generic "Connection closed by UNKNOWN port 65535" message.

**Key design decision (deadlock prevention)**: server→w runs in the main goroutine and returns immediately on server EOF. The r→server goroutine is fire-and-forget. This prevents a deadlock where:
- The remote target closes (e.g., sshd sends keyboard-interactive prompt and closes)
- The SSH client is waiting for the prompt and won't close stdin
- The proxy would deadlock waiting for stdin EOF that never comes

When the reader reaches EOF first (user finishes typing), the connection is closed (full close — HTTP transport has no half-close).

### `ServeListener(ctx, ln)`
Accepts local TCP connections and proxies each to a fresh server connection. Each connection gets its own `Dial`. Closes the server connection when either direction hits EOF.

### `Dial(ctx)`
Public method that establishes an HTTP transport connection to the server via `transport.DialConnect`. Sends agent ID and target as query parameters. Returns a `net.Conn` that can be used for bidirectional proxy.

## Testing

### `TestServeRW_ServerClosesReturns`
Reproduction test for the SSH ProxyCommand deadlock. Creates a send-and-close target (simulating sshd), connects via pipe-based yamux, verifies ServeRW returns within 5 s without closing stdin.

### `TestServeRW_HandshakeFailureWritesToWriter`
Verifies that connection failures write a user-friendly error to `w` (stdout) so SSH clients can display it.

### `TestConnectClient_LocalPortMode`
Full integration test: server + agent + connect client + echo target over real TCP loopback.

## Rules
* **Never wait for both copy directions to finish** in `ServeRW` — the server→w direction drives the return.
* **Always use `io.CopyBuffer` with the `bufferPool`** (32 KiB pooled buffers) for proxy copies.
* **HTTP transport**: Uses `transport.DialConnect` (SSE-down + POST-up) instead of TCP dial + handshake.
* **Error visibility**: Write errors to both stderr and stdout so SSH ProxyCommand users see them.
