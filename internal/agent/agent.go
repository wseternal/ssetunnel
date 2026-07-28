package agent

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/wseternal/ssetunnel/internal/mux"
	"github.com/wseternal/ssetunnel/internal/transport"
)

// Agent runs inside the restricted network: it dials out to the server,
// multiplexes streams over the tunnel, forwards them to the configured
// TCP target, and reconnects automatically on drops (spec: agents
// recover from drops automatically).
type Agent struct {
	ServerURL  string        // tunnel server base URL
	Target     string        // TCP address to forward streams to
	MaxBackoff time.Duration // reconnect cap; 0 → 1 s
	MaxWait    time.Duration // batcher flush ceiling; 0 → default
	Client     *http.Client  // nil → transport default
}

// Run connects and reconnects until ctx is canceled. The first retry is
// immediate (interactive-first profile); backoff then doubles from 50 ms
// up to MaxBackoff (plan step 7). A session that survived past the
// health threshold resets the backoff: a drop after long uptime was a
// network event, not a flapping server, so retry immediately.
func (a *Agent) Run(ctx context.Context) error {
	maxBackoff := a.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = time.Second
	}
	const healthThreshold = 10 * time.Second
	delay := time.Duration(0)
	for {
		start := time.Now()
		if err := a.runOnce(ctx); err != nil {
			log.Printf("agent: %v; reconnecting", err)
		} else {
			log.Printf("agent: session ended; reconnecting")
		}
		if time.Since(start) >= healthThreshold {
			delay = 0
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil
		}
		if delay == 0 {
			delay = 50 * time.Millisecond
		} else {
			delay *= 2
		}
		if delay > maxBackoff {
			delay = maxBackoff
		}
	}
}

// runOnce holds one session: connect → yamux client → accept streams →
// dial target → proxy. It returns when the session dies so Run can
// reconnect with a fresh session ID.
func (a *Agent) runOnce(ctx context.Context) error {
	conn, err := transport.DialAgent(ctx, transport.Config{
		URL:     a.ServerURL,
		MaxWait: a.MaxWait,
		Client:  a.Client,
	})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
	sess, err := mux.Client(conn)
	if err != nil {
		return fmt.Errorf("mux client: %w", err)
	}
	defer sess.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			sess.Close()
		case <-done:
		}
	}()
	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			return nil // session died; Run reconnects
		}
		go a.proxy(stream)
	}
}

// proxy forwards one stream to the configured target, bidirectionally.
func (a *Agent) proxy(stream net.Conn) {
	target, err := net.DialTimeout("tcp", a.Target, 10*time.Second)
	if err != nil {
		log.Printf("agent: dial target %s: %v", a.Target, err)
		stream.Close()
		return
	}
	go func() { io.Copy(target, stream); target.Close() }()
	go func() { io.Copy(stream, target); stream.Close() }()
}
