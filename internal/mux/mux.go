package mux

import (
	"io"
	"time"

	"github.com/hashicorp/yamux"
)

// config returns the yamux config for both ends (plan decision 6).
func config() *yamux.Config {
	cfg := yamux.DefaultConfig()
	// 4 MiB stream window: at 100 ms effective RTT this yields
	// 4 MiB / 100 ms = 40 MB/s theoretical, enough for VNC
	// framebuffer uploads and large file transfers.
	cfg.MaxStreamWindowSize = 4 << 20
	// 30 s keepalive: detects half-open peers; the SSE heartbeats
	// (15 s) already keep middleboxes from going idle, so this only
	// needs to catch dead agents, not preserve the connection.
	cfg.KeepAliveInterval = 30 * time.Second
	// AcceptBacklog 256: absorb agent-listener accept bursts without
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
