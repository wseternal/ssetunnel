package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/wseternal/ssetunnel/internal/metrics"
	"github.com/wseternal/ssetunnel/internal/mux"
	"github.com/wseternal/ssetunnel/internal/transport"
)

// Agent runs inside the restricted network: it dials out to the server,
// multiplexes streams over the tunnel, forwards them to the configured
// TCP target, and reconnects automatically on drops (spec: agents
// recover from drops automatically).
type Agent struct {
	ServerURL  string        // tunnel server base URL
	BasePath   string        // HTTP path prefix for tunnel endpoints (e.g. "/tunnel"); empty means no prefix
	Target     string        // TCP address to forward streams to (empty = dynamic mode)
	AgentID    string        // human-readable agent identifier (e.g. "mydevbox")
	Token      string        // Bearer token or single-use PIN for authentication
	MaxBackoff time.Duration // reconnect cap; 0 → 30 s
	MaxWait    time.Duration // batcher flush ceiling; 0 → default
	Client     *http.Client  // nil → transport default

	// RequestModifier, when set, takes precedence over Token.
	// Used for session-based auth where the token is loaded from a file.
	RequestModifier func(*http.Request)

	// Cycle-2 upstream knobs (negotiated down to the server's
	// advertisement; zero values give cycle-1 serial behavior at 256 KiB batches).
	BatchSize   int  // upstream batch ceiling; 0 → transport default
	Concurrency int  // upstream POST sender depth; 0 → 1 (serial)
	Compress    bool // negotiate gzip-per-batch

	// NoAutoTune disables the server's auto-tuning: event: tune frames
	// are ignored and the agent keeps its static CLI flags.
	NoAutoTune bool
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
		URL:              a.ServerURL,
		BasePath:         a.BasePath,
		Token:            a.Token,
		RequestModifier:  a.RequestModifier,
		MaxWait:          a.MaxWait,
		Client:           a.Client,
		MaxBatchSize:     a.BatchSize,
		Concurrency:      a.Concurrency,
		Compress:         a.Compress,
		AgentID:          a.AgentID,
		WantTargetHeader: a.Target == "", // dynamic mode: read target from stream
		OnTokenUpgrade: func(newToken string) {
			a.Token = newToken
			log.Printf("agent: PIN redeemed, upgraded to persistent token")
		},
	})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	// Wire auto-tuning: parse event: tune JSON and apply batch size changes.
	// Concurrency changes are deferred to reconnect (v1 limitation).
	if !a.NoAutoTune {
		conn.OnTune = func(data []byte) {
			var params metrics.TransportParams
			if err := json.Unmarshal(data, &params); err != nil {
				log.Printf("agent: bad tune frame: %v", err)
				return
			}
			log.Printf("agent: auto-tune: batch_size=%d compress=%v concurrency=%d",
				params.BatchSize, params.Compress, params.Concurrency)
			conn.ApplyTune(params.BatchSize, params.Compress)
		}
	}
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

// proxy forwards one stream to a target, bidirectionally.
// In fixed-target mode (a.Target != ""), dials a.Target directly.
// In dynamic mode (a.Target == ""), reads the target address from the
// first \n-terminated line of the stream, then dials it.
func (a *Agent) proxy(stream net.Conn) {
	target := a.Target

	if target == "" {
		// Dynamic mode: read target from stream header.
		reader := bufio.NewReader(stream)
		line, err := reader.ReadString('\n')
		if err != nil {
			log.Printf("agent: read target header: %v", err)
			stream.Close()
			return
		}
		target = strings.TrimSpace(line)
		if target == "" || target == "*" {
			log.Printf("agent: empty or wildcard target header, closing stream")
			stream.Close()
			return
		}

		// Wrap stream to preserve buffered data for the bidirectional copy.
		// Any bytes the bufio.Reader consumed beyond the \n are prepended
		// to subsequent reads from the stream.
		stream = &readerConn{Reader: reader, Conn: stream}
	}

	// Shell target: spawn a local shell with PTY instead of dialing TCP.
	if target == TargetShell {
		a.proxyShell(stream)
		return
	}

	conn, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		log.Printf("agent: dial target %s: %v", target, err)
		stream.Close()
		return
	}
	go func() { io.Copy(conn, stream); conn.Close() }()
	go func() { io.Copy(stream, conn); stream.Close() }()
}

// readerConn wraps a bufio.Reader over a net.Conn so that bytes already
// buffered by the reader are not lost when switching from header parsing
// to raw io.Copy.
type readerConn struct {
	*bufio.Reader
	net.Conn
}

func (r *readerConn) Read(b []byte) (int, error) {
	return r.Reader.Read(b)
}

func (r *readerConn) Close() error {
	return r.Conn.Close()
}
