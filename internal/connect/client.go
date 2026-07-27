package connect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/wseternal/ssetunnel/internal/transport"
)

var bufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 32*1024)
		return &buf
	},
}

// Client connects to a tunnel server's HTTP /connect endpoint and proxies
// bidirectionally between the server's transport (SSE-down + POST-up) and
// a local reader/writer or TCP listener.
type Client struct {
	serverURL string // tunnel server base URL, e.g. http://host:8080
	token     string
	agentID   string // agent routing key (empty = first-match)
	target    string // dynamic target address (empty = no dynamic target)
	basePath  string // HTTP path prefix for tunnel endpoints (empty = no prefix)

	// BatchSize is the upstream batch ceiling; 0 → 1024.
	BatchSize int
	// MaxWait is the batcher flush ceiling; 0 → 10ms.
	MaxWait time.Duration
}

func NewClient(serverURL, token, agentID, target, basePath string) *Client {
	return &Client{
		serverURL: serverURL,
		token:     token,
		agentID:   agentID,
		target:    target,
		basePath:  basePath,
	}
}

func (c *Client) ServeListener(ctx context.Context, ln net.Listener) error {
	// Eagerly validate the token by performing a test connection at startup.
	// This catches invalid tokens immediately instead of silently failing
	// on the first user connection.
	if c.token != "" {
		probe, err := c.dial(ctx)
		if err != nil {
			return fmt.Errorf("token validation failed: %w", err)
		}
		probe.Close()
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go c.handleLocalConn(ctx, conn)
	}
}

func (c *Client) ServeStdio(ctx context.Context) error {
	return c.ServeRW(ctx, os.Stdin, os.Stdout)
}

// ServeRW connects to the tunnel server via HTTP transport and copies
// bidirectionally between the provided reader/writer and the server
// connection. When the server side closes (EOF on read), the connection
// is torn down immediately. When the reader reaches EOF first, the
// connection is closed (full close — HTTP transport has no half-close).
func (c *Client) ServeRW(ctx context.Context, r io.Reader, w io.Writer) error {
	serverConn, err := c.dial(ctx)
	if err != nil {
		// Print a user-friendly error to stderr for direct terminal users,
		// and to w (stdout in ProxyCommand mode) so the SSH client displays
		// it before the generic "Connection closed by UNKNOWN port 65535".
		msg := fmt.Sprintf("ssetunnel: %s\n", err)
		fmt.Fprint(os.Stderr, msg)
		fmt.Fprint(w, msg)
		return fmt.Errorf("stdio connect failed: %w", err)
	}
	defer serverConn.Close()

	type result struct{ err error }
	done := make(chan result, 1)

	// r → server in the background. When the reader reaches EOF (e.g.,
	// stdin closes in ProxyCommand mode), close the server connection to
	// signal EOF to the server. HTTP transport has no half-close, so a
	// full close is the only way to propagate upstream EOF.
	go func() {
		buf := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(buf)
		io.CopyBuffer(serverConn, r, *buf)
		serverConn.Close()
		done <- result{}
	}()

	// server → w: when the server side closes (EOF or error), return
	// immediately. This tears down the process (in ProxyCommand mode),
	// closing stdout so the parent process (SSH client) sees EOF and
	// can react.
	buf := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(buf)
	_, err = io.CopyBuffer(w, serverConn, *buf)
	if err != nil {
		// "use of closed network connection" means the goroutine above
		// closed the conn after reader EOF — normal termination, not
		// an error. Same for io.ErrClosedPipe.
		if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
			return nil
		}
		return err
	}
	return nil
}

func (c *Client) handleLocalConn(ctx context.Context, localConn net.Conn) {
	defer localConn.Close()

	// Retry dial with exponential backoff so transient
	// server outages don't immediately kill the user's connection.
	var serverConn net.Conn
	err := backoff.Retry(func() error {
		if ctx.Err() != nil {
			return backoff.Permanent(ctx.Err())
		}
		var dialErr error
		serverConn, dialErr = c.dial(ctx)
		if dialErr != nil {
			log.Printf("connect: dial failed (will retry): %v", dialErr)
			return dialErr
		}
		return nil
	}, backoff.WithContext(clientBackoff(), ctx))
	if err != nil {
		log.Printf("connect: dial failed after retries: %v", err)
		return
	}
	defer serverConn.Close()

	go func() {
		buf := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(buf)
		_, _ = io.CopyBuffer(serverConn, localConn, *buf)
		_ = serverConn.Close()
	}()

	buf := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(buf)
	_, _ = io.CopyBuffer(localConn, serverConn, *buf)
}

// clientBackoff builds the exponential backoff strategy for connect
// client retries: 500 ms initial, 1.5× multiplier, 30 s total cap.
func clientBackoff() *backoff.ExponentialBackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 500 * time.Millisecond
	b.MaxInterval = 10 * time.Second
	b.MaxElapsedTime = 30 * time.Second
	b.Multiplier = 1.5
	b.RandomizationFactor = 0.1
	return b
}

// dial connects to the tunnel server's HTTP /connect endpoint using the
// SSE-down + POST-up transport. The returned net.Conn proxies data through
// the server to the agent's yamux stream.
func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	return c.Dial(ctx)
}

// Dial connects to the tunnel server's HTTP /connect endpoint using the
// SSE-down + POST-up transport. The returned net.Conn proxies data through
// the server to the agent's yamux stream.
func (c *Client) Dial(ctx context.Context) (net.Conn, error) {
	batchSize := c.BatchSize
	if batchSize <= 0 {
		batchSize = 1024
	}
	maxWait := c.MaxWait
	if maxWait <= 0 {
		maxWait = 10 * time.Millisecond
	}

	conn, err := transport.DialConnect(ctx, transport.Config{
		URL:          c.serverURL,
		BasePath:     c.basePath,
		Token:        c.token,
		AgentID:      c.agentID,
		Target:       c.target,
		MaxBatchSize: batchSize,
		MaxWait:      maxWait,
	})
	if err != nil {
		return nil, fmt.Errorf("dial server %s: %w", c.serverURL, err)
	}
	return conn, nil
}
