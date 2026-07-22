package mux

import (
	"io"
	"time"

	"github.com/hashicorp/yamux"
)

// config returns the yamux config for both ends (plan decision 6).
func config() *yamux.Config {
	cfg := yamux.DefaultConfig()
	// 1 MiB stream window: the 256 KiB default caps throughput at
	// 256 KiB / 100 ms effective RTT = 2.5 MB/s, below the 5 MB/s
	// budget; 1 MiB keeps the budget reachable at hostile proxy RTTs.
	cfg.MaxStreamWindowSize = 1 << 20
	// 30 s keepalive: detects half-open peers; the SSE heartbeats
	// (15 s) already keep middleboxes from going idle, so this only
	// needs to catch dead agents, not preserve the connection.
	cfg.KeepAliveInterval = 30 * time.Second
	// AcceptBacklog 256: absorb entry-listener accept bursts without
	// dropping SYNs; far above the 32-stream concurrency target.
	cfg.AcceptBacklog = 256
	return cfg
}

// Server wraps conn (the server side of the tunnel) in a yamux session.
func Server(conn io.ReadWriteCloser) (*yamux.Session, error) {
	return yamux.Server(conn, config())
}

// Client wraps conn (the agent side of the tunnel) in a yamux session.
func Client(conn io.ReadWriteCloser) (*yamux.Session, error) {
	return yamux.Client(conn, config())
}
