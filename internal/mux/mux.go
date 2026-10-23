package mux

import (
	"io"
	"time"

	"github.com/hashicorp/yamux"
)

// config returns the yamux config for both ends.
//
// Keepalive tuning for a store-and-forward HTTP transport:
//
//   - KeepAliveInterval = 60 s (up from default 30 s). The SSE heartbeats
//     (15 s) already keep middleboxes from going idle; the yamux ping only
//     needs to detect a dead peer, not preserve the connection.
//   - ConnectionWriteTimeout = 120 s (up from default 10 s). On a slow
//     uplink, yamux control frames (pings, pongs, window updates) queue
//     FIFO behind up to 4 MiB of buffered bulk output in the agent's
//     batcher. At 100 KiB/s drain rate that is ~40 s of queueing; the
//     10 s default kills the session on a normal vim redraw burst. 120 s
//     covers a 4 MiB queue at ~35 KiB/s with margin.
func config() *yamux.Config {
	cfg := yamux.DefaultConfig()
	// 4 MiB stream window: at 100 ms effective RTT this yields
	// 4 MiB / 100 ms = 40 MB/s theoretical, enough for VNC
	// framebuffer uploads and large file transfers.
	cfg.MaxStreamWindowSize = 4 << 20
	cfg.KeepAliveInterval = 60 * time.Second
	cfg.ConnectionWriteTimeout = 120 * time.Second
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
