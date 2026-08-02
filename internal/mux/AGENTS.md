# Multiplexer (mux)

Thin wrapper around `github.com/hashicorp/yamux` with tuned configuration for the tunnel's throughput and latency budget.

## API

```go
func Server(conn io.ReadWriteCloser) (*yamux.Session, error)  // server side (over Session)
func Client(conn io.ReadWriteCloser) (*yamux.Session, error)  // agent side (over Conn)
```

## Tuned Configuration

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| `MaxStreamWindowSize` | 4 MiB | At 100 ms effective RTT this yields 40 MB/s theoretical — enough for VNC framebuffer uploads and large file transfers. |
| `KeepAliveInterval` | 30 s | Detects half-open peers. SSE heartbeats (15 s) already keep middleboxes alive. |
| `AcceptBacklog` | 256 | Absorbs agent session accept bursts. Far above the 32-stream concurrency target. |

## Rules
* Both ends must use the same `config()` — mismatched window sizes cause flow-control stalls.
* yamux `stream.Close()` kills both directions — no half-close support. Use TCP `CloseWrite()` on the agent connection for half-close signaling.
