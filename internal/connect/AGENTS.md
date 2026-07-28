# Connect Client

The connect client is the user-side component that bridges local applications (SSH clients, DB tools) to the tunnel server's entry listener.

## Architecture

```
SSH ProxyCommand (--local -)          Local Port Mode (--local 127.0.0.1:3306)
         │                                      │
   ServeStdio/ServeRW                    ServeListener
         │                                      │
   dialAndHandshake                      dialAndHandshake (per connection)
         │                                      │
   io.Copy (stdin↔server)                io.Copy (localConn↔server)
```

## Core Types

### `Client`
Holds `serverEntryAddr`, `token`, `agentID` (for routing), and `target` (for dynamic target mode). Created via `NewClient(addr, token, agentID, target)`.

### `ServeRW(ctx, r, w)`
The critical function for SSH ProxyCommand support. Copies bidirectionally between `r`/`w` (typically stdin/stdout pipes) and the server connection.

**Error handling**: On handshake failure, writes the error to both `os.Stderr` and `w` (stdout) so the SSH client displays the actual error before its generic "Connection closed by UNKNOWN port 65535" message.

**Key design decision (deadlock prevention)**: server→w runs in the main goroutine and returns immediately on server EOF. The r→server goroutine is fire-and-forget. This prevents a deadlock where:
- The remote target closes (e.g., sshd sends keyboard-interactive prompt and closes)
- The SSH client is waiting for the prompt and won't close stdin
- The proxy would deadlock waiting for stdin EOF that never comes

When the reader reaches EOF first (user finishes typing), a TCP half-close (`CloseWrite`) signals EOF to the server without killing the read path.

### `ServeListener(ctx, ln)`
Accepts local TCP connections and proxies each to a fresh server connection. Each connection gets its own `dialAndHandshake`. Closes the server connection when either direction hits EOF.

### `dialAndHandshake(ctx)`
Dials the entry listener TCP, optionally sends a handshake line `TOKEN [agent_id [target]]\n`, waits for "OK\n" response. Uses a 10 s dial timeout and 5 s handshake read deadline. Strips the `ERR ` prefix from error responses for cleaner user-facing messages.

## Testing

### `TestServeRW_ServerClosesReturns`
Reproduction test for the SSH ProxyCommand deadlock. Creates a send-and-close target (simulating sshd), connects via pipe-based yamux, verifies ServeRW returns within 5 s without closing stdin.

### `TestServeRW_HandshakeFailureWritesToWriter`
Verifies that handshake failures write a user-friendly error to `w` (stdout) so SSH clients can display it.

### `TestConnectClient_LocalPortMode`
Full integration test: server + agent + connect client + echo target over real TCP loopback.

## Rules
* **Never wait for both copy directions to finish** in `ServeRW` — the server→w direction drives the return.
* **Always use `io.CopyBuffer` with the `bufferPool`** (32 KiB pooled buffers) for proxy copies.
* **Handshake protocol**: send `"TOKEN [agent_id [target]]\n"`, read `"OK\n"` or `"ERR <msg>\n"`.
* **Error visibility**: Write errors to both stderr and stdout so SSH ProxyCommand users see them.
