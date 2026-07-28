package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// downPipeCap bounds downstream bytes buffered for the local reader
// before backpressure stalls the SSE stream.
const downPipeCap = 256 << 10

// Config configures DialAgent.
type Config struct {
	URL            string        // tunnel server base URL, e.g. http://host:port
	SessionID      string        // 128-bit random hex; generated when empty
	Client         *http.Client  // nil → default tunnel client
	MaxBatchSize   int           // 0 → DefaultMaxBatchSize
	MaxWait        time.Duration // 0 → DefaultMaxWait
	MaxQueuedBytes int           // Write blocks past this; 0 → DefaultMaxQueuedBytes
}

// Conn is the agent side of the tunnel: a net.Conn over SSE-down +
// batched-POST-up (plan decisions 1+4). It owns both goroutines; every
// goroutine hangs off a context owned by the conn (decision 9).
type Conn struct {
	client    *http.Client
	ownClient bool // Close must reap the transport's idle conns
	upURL     string
	id        string
	seq       atomic.Uint64 // next X-SSET-Seq; serial sender keeps order

	ctx    context.Context
	cancel context.CancelFunc

	batcher *Batcher // Write → batches → single serial POST sender
	down    *Pipe    // SSE events → Read

	writeMu       sync.Mutex
	writeDeadline time.Time

	closeOnce sync.Once
	wg        sync.WaitGroup // readLoop
}

// DialAgent opens the SSE downstream, starts the batching POST sender,
// and returns the conn. The dial fails fast if the server rejects the
// events stream.
func DialAgent(ctx context.Context, cfg Config) (*Conn, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("transport: Config.URL is required")
	}
	id := cfg.SessionID
	if id == "" {
		var b [16]byte // 128-bit random session ID (plan decision 5)
		if _, err := rand.Read(b[:]); err != nil {
			return nil, fmt.Errorf("generate session id: %w", err)
		}
		id = hex.EncodeToString(b[:])
	}
	client := cfg.Client
	if client == nil {
		// Plan decision 7: explicit MaxIdleConnsPerHost, no compression
		// (SSE must not be gzip-buffered by the transport).
		client = &http.Client{Transport: &http.Transport{
			MaxIdleConnsPerHost: 4, // SSE + serial POST + reconnect headroom
			DisableCompression:  true,
		}}
	}
	maxSize := cfg.MaxBatchSize
	if maxSize <= 0 {
		maxSize = DefaultMaxBatchSize
	}
	maxWait := cfg.MaxWait
	if maxWait <= 0 {
		maxWait = DefaultMaxWait
	}

	c := &Conn{
		client: client,
		upURL:  cfg.URL + "/up",
		id:     id,
		down:   NewPipe(downPipeCap),
	}
	c.ownClient = cfg.Client == nil
	c.ctx, c.cancel = context.WithCancel(ctx)

	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet,
		cfg.URL+"/events?id="+url.QueryEscape(id), nil)
	if err != nil {
		c.cancel()
		return nil, fmt.Errorf("build events request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		c.cancel()
		return nil, fmt.Errorf("open events stream: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body) // always drain (decision 7)
		resp.Body.Close()
		c.cancel()
		return nil, fmt.Errorf("open events stream: status %s", resp.Status)
	}

	c.batcher = NewBatcher(maxSize, maxWait, cfg.MaxQueuedBytes, c.send)
	c.wg.Add(1)
	go c.readLoop(resp.Body)
	return c, nil
}

// SessionID returns the agent-generated session ID.
func (c *Conn) SessionID() string { return c.id }

// readLoop decodes the SSE stream into the down pipe. Heartbeats are
// filtered by the codec and never surface as data. Stream death kills
// the conn (fail-fast, decision 3).
func (c *Conn) readLoop(body io.ReadCloser) {
	defer c.wg.Done()
	defer body.Close()
	dec := newSSEDecoder()
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			events, derr := dec.Feed(buf[:n])
			if derr != nil {
				c.down.CloseWithError(fmt.Errorf("decode events stream: %w", derr))
				go c.Close() // async: Close waits on this goroutine
				return
			}
			for _, ev := range events {
				if _, werr := c.down.Write(ev); werr != nil {
					return // conn closed
				}
			}
		}
		if err != nil {
			rerr := err
			if c.ctx.Err() != nil {
				rerr = net.ErrClosed // closed by us, not by the peer
			}
			c.down.CloseWithError(rerr)
			go c.Close()
			return
		}
	}
}

// send is the batcher's flush func: one serial POST per batch
// (decision 1). The write deadline maps to the POST request context
// (decision 8). Any POST failure kills the conn (decision 3).
func (c *Conn) send(batch []byte) error {
	ctx := c.ctx
	c.writeMu.Lock()
	dl := c.writeDeadline
	c.writeMu.Unlock()
	if !dl.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.upURL, bytes.NewReader(batch))
	if err != nil {
		go c.Close()
		return fmt.Errorf("build upstream post: %w", err)
	}
	req.Header.Set("X-SSET-Session", c.id)
	req.Header.Set("X-SSET-Seq", strconv.FormatUint(c.seq.Add(1)-1, 10))
	resp, err := c.client.Do(req)
	if err != nil {
		go c.Close()
		return fmt.Errorf("post upstream batch: %w", err)
	}
	io.Copy(io.Discard, resp.Body) // always drain (decision 7)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		go c.Close()
		return fmt.Errorf("post upstream batch: status %s", resp.Status)
	}
	return nil
}

// Read returns downstream bytes from the SSE stream.
func (c *Conn) Read(b []byte) (int, error) { return c.down.Read(b) }

// Write buffers b for the next upstream POST. A sticky POST failure
// surfaces here (decision 3: POST failure = session death).
func (c *Conn) Write(b []byte) (int, error) {
	if err := c.batcher.Err(); err != nil {
		return 0, err
	}
	return c.batcher.Write(b)
}

// Close cancels the conn context first — in-flight and queued POSTs
// abort immediately, so a peer that accepts a POST but never responds
// cannot hang Close (dropped bytes at Close are consistent with the
// fail-fast model, plan decision 3/9). Then it stops the read pipe,
// drains the batcher, and waits for the read goroutine. Idempotent.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		c.down.CloseWithError(net.ErrClosed)
		c.batcher.Close()
		c.wg.Wait()
		// A client we created owns its transport: reap idle keep-alive
		// conns or every reconnect leaks their goroutines (plan decision
		// 9: no goroutine leak across reconnect cycles).
		if c.ownClient {
			if t, ok := c.client.Transport.(*http.Transport); ok {
				t.CloseIdleConnections()
			}
		}
	})
	return nil
}

// tunnelAddr is a placeholder net.Addr for the tunnel conn.
type tunnelAddr string

func (a tunnelAddr) Network() string { return "ssetunnel" }
func (a tunnelAddr) String() string  { return string(a) }

// LocalAddr reports the agent-side endpoint.
func (c *Conn) LocalAddr() net.Addr { return tunnelAddr("agent/" + c.id) }

// RemoteAddr reports the server endpoint.
func (c *Conn) RemoteAddr() net.Addr { return tunnelAddr(c.upURL) }

// SetDeadline sets read and write deadlines.
func (c *Conn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

// SetReadDeadline sets the downstream (Read) deadline, honored by the
// read pipe's mutex-guarded timer + select (decision 8).
func (c *Conn) SetReadDeadline(t time.Time) error { return c.down.SetReadDeadline(t) }

// SetWriteDeadline sets the upstream (Write) deadline; it maps to the
// POST request context (decision 8).
func (c *Conn) SetWriteDeadline(t time.Time) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.writeDeadline = t
	return nil
}

var _ net.Conn = (*Conn)(nil)
