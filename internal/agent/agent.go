package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v4"
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
	Token      string        // Bearer token or single-use PIN for authentication
	MaxBackoff time.Duration // reconnect cap; 0 → 30 s
	MaxWait    time.Duration // batcher flush ceiling; 0 → default
	Client     *http.Client  // nil → transport default

	// Cycle-2 upstream knobs (negotiated down to the server's
	// advertisement; zero values give cycle-1 serial 16 KiB behavior).
	BatchSize   int  // upstream batch ceiling; 0 → transport default
	Concurrency int  // upstream POST sender depth; 0 → 1 (serial)
	Compress    bool // negotiate gzip-per-batch
}

// Run connects and reconnects until ctx is canceled. Reconnect uses
// exponential backoff (500 ms → 30 s cap with jitter) via cenkalti/backoff.
// A session that survived past the health threshold resets the backoff:
// a drop after long uptime was a network event, not a flapping server,
// so reconnect immediately.
func (a *Agent) Run(ctx context.Context) error {
	b := newAgentBackoff()
	if a.MaxBackoff > 0 {
		b.MaxInterval = a.MaxBackoff
	}
	const healthThreshold = 10 * time.Second
	for {
		start := time.Now()
		err := a.runOnce(ctx)

		// Unrecoverable errors (e.g. bad token) — exit immediately
		// instead of spinning on retries that will never succeed.
		if errors.Is(err, transport.ErrUnauthorized) {
			log.Printf("agent: %v; giving up", err)
			return err
		}
		if err != nil {
			log.Printf("agent: %v; reconnecting", err)
		} else {
			log.Printf("agent: session ended; reconnecting")
		}
		if ctx.Err() != nil {
			return nil
		}

		// A long-lived healthy session resets backoff: reconnect
		// immediately since the drop was likely transient.
		if time.Since(start) >= healthThreshold {
			b.Reset()
			continue
		}

		delay := b.NextBackOff()
		if delay == backoff.Stop {
			delay = 500 * time.Millisecond
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil
		}
	}
}

// newAgentBackoff builds the exponential backoff strategy for agent
// reconnection: 500 ms initial, 2× multiplier, 30 s cap, no elapsed
// ceiling (retry forever), 10 % jitter.
func newAgentBackoff() *backoff.ExponentialBackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 500 * time.Millisecond
	b.MaxInterval = 30 * time.Second
	b.MaxElapsedTime = 0 // never stop
	b.Multiplier = 2.0
	b.RandomizationFactor = 0.1
	return b
}

// runOnce holds one session: connect → yamux client → accept streams →
// dial target → proxy. It returns when the session dies so Run can
// reconnect with a fresh session ID.
func (a *Agent) runOnce(ctx context.Context) error {
	conn, err := transport.DialAgent(ctx, transport.Config{
		URL:          a.ServerURL,
		Token:        a.Token,
		MaxWait:      a.MaxWait,
		Client:       a.Client,
		MaxBatchSize: a.BatchSize,
		Concurrency:  a.Concurrency,
		Compress:     a.Compress,
		OnTokenUpgrade: func(newToken string) {
			a.Token = newToken
			log.Printf("agent: PIN redeemed, upgraded to persistent token")
		},
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
