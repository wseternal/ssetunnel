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
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/wseternal/ssetunnel/internal/metrics"
	"github.com/wseternal/ssetunnel/internal/mux"
	"github.com/wseternal/ssetunnel/internal/remoteapp"
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

	// Verbose enables detailed tracing of shell session lifecycle,
	// data flow (byte counts, periodic summaries), and control
	// channel activity. Useful for diagnosing cloud shell issues.
	Verbose bool

	// OnTokenRefresh, when set, is called before each reconnect attempt
	// to refresh the session token. It returns the new raw token or an
	// error. On success the agent's Token field is updated; on failure
	// the current token is kept and the reconnect proceeds (the server
	// will reject an actually-expired token with 401).
	OnTokenRefresh func() (newToken string, err error)

	// registry maps yamux stream IDs to data stream handlers so the
	// control dispatcher can route resize/trace messages.
	registry *streamRegistry

	// controlMu protects sendControl and controlGen. The control handler
	// installs and clears the sender; data stream goroutines read it to
	// push trace events. controlGen is a monotonic counter used to
	// detect stale send cleanup across reconnects.
	controlMu   sync.RWMutex
	sendControl controlSender
	controlGen  int

	// traceDropLogged suppresses repeated "trace dropped" logs.
	// Reset when a new control stream is established.
	traceDropLogged atomic.Bool

	// logBuf captures recent log output so the server can request it
	// via the control channel (MsgLogRequest). Installed as a tee
	// target in Run().
	logBuf *logBuffer
}

// Run connects and reconnects until ctx is canceled. Reconnect uses
// exponential backoff (500 ms → 30 s cap with jitter) via cenkalti/backoff.
// A session that survived past the health threshold resets the backoff:
// a drop after long uptime was a network event, not a flapping server,
// so reconnect immediately.
func (a *Agent) Run(ctx context.Context) error {
	// Install log capture: all log.Printf output goes to both stderr
	// and the ring buffer. The server can request recent lines via
	// the control channel (MsgLogRequest).
	a.logBuf = newLogBuffer(256)
	log.SetOutput(io.MultiWriter(os.Stderr, a.logBuf))

	b := newAgentBackoff()
	if a.MaxBackoff > 0 {
		b.MaxInterval = a.MaxBackoff
	}
	const healthThreshold = 10 * time.Second
	for {
		// Refresh token before each (re)connect to keep long-lived
		// sessions valid. Failure is non-fatal — the current token
		// may still be accepted by the server.
		a.RefreshToken()

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

// RefreshToken calls OnTokenRefresh to obtain a new session token.
// On success it updates Agent.Token; on failure (or when OnTokenRefresh
// is nil) it logs a warning and keeps the current token.
func (a *Agent) RefreshToken() {
	if a.OnTokenRefresh == nil {
		return
	}
	newToken, err := a.OnTokenRefresh()
	if err != nil {
		log.Printf("agent: token refresh failed: %v (continuing with current token)", err)
		return
	}
	if newToken != "" && newToken != a.Token {
		a.Token = newToken
		log.Printf("agent: session token refreshed")
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
	a.registry = newStreamRegistry()

	// Open the control stream: agent → server. The server's accept
	// loop picks it up and identifies it by the marker. This runs
	// before the data stream accept loop so the control channel is
	// ready before any data streams arrive.
	a.openControlStream(sess)

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
			// Context canceled = clean shutdown (Run will check ctx.Err()
			// and return without logging). Otherwise, return the error
			// so Run can log the actual reason the session died.
			if ctx.Err() != nil {
				return nil
			}
			// Enrich the error with transport-level context. The SSE
			// stream close reason (ReadErr) is the most common root
			// cause: the server closed the HTTP response, killing the
			// agent's yamux session via the underlying conn.
			if rerr := conn.ReadErr(); rerr != nil {
				return fmt.Errorf("accept stream: %w (sse: %v)", err, rerr)
			}
			return fmt.Errorf("accept stream: %w", err)
		}
		go a.acceptStream(stream)
	}
}

// acceptStream handles a newly accepted yamux data stream. The control
// stream is opened separately in openControlStream and never reaches
// this function. In dynamic mode (a.Target == ""), the server writes
// the target address as the first line. In fixed mode, the stream
// carries raw data with no header.
func (a *Agent) acceptStream(stream net.Conn) {
	sid := getStreamID(stream)
	if a.Verbose {
		verboseTrace("stream accepted: yamux_id=%d dynamic=%v", sid, a.Target == "")
	}
	// In dynamic mode, read the target header line from the server.
	if a.Target == "" {
		reader := bufio.NewReader(stream)
		line, err := reader.ReadString('\n')
		if err != nil {
			log.Printf("agent: read target header: %v", err)
			stream.Close()
			return
		}
		target := strings.TrimSpace(line)
		if a.Verbose {
			verboseTrace("stream target header: yamux_id=%d target=%q", sid, target)
		}
		wrapped := &readerConn{Reader: reader, Conn: stream}
		a.proxyStream(wrapped, target)
		return
	}

	// Fixed mode: proxy directly, no header to read.
	a.proxyStream(stream, a.Target)
}

// proxyStream forwards one data stream to a target, bidirectionally.
// target is the trimmed first line of the stream (from acceptStream).
// In fixed-target mode (a.Target != ""), target is ignored and a.Target
// is used directly.
func (a *Agent) proxyStream(stream net.Conn, target string) {
	// In fixed-target mode, override the target from the stream header.
	if a.Target != "" {
		target = a.Target
	}

	sid := getStreamID(stream)

	if target == "" || target == "*" {
		log.Printf("agent: empty or wildcard target header, closing stream")
		stream.Close()
		return
	}

	if a.Verbose {
		verboseTrace("stream routing: yamux_id=%d target=%q", sid, target)
	}

	// Shell target: spawn a local shell with PTY instead of dialing TCP.
	if target == TargetShell {
		if a.Verbose {
			verboseTrace("shell proxy entering: yamux_id=%d", sid)
		}
		proxyShell(stream, sid, a)
		if a.Verbose {
			verboseTrace("shell proxy exited: yamux_id=%d", sid)
		}
		return
	}

	// Remote app target: screen capture + input replay via robotgo.
	if target == TargetRemoteApp {
		remoteapp.ProxyRemoteApp(stream)
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
