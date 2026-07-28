package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// downPipeCap bounds downstream bytes buffered for the local reader
// before backpressure stalls the SSE stream.
const downPipeCap = 256 << 10

// ErrUnauthorized is returned by DialAgent when the server rejects the
// agent with 401 Unauthorized. This is an unrecoverable error — the
// token is invalid and retrying will not help.
var ErrUnauthorized = errors.New("unauthorized: server rejected token with 401")

// Config configures DialAgent.
type Config struct {
	URL            string        // tunnel server base URL, e.g. http://host:port
	BasePath       string        // HTTP path prefix prepended to all endpoints (e.g. "/tunnel"); empty means no prefix
	SessionID      string        // 128-bit random hex; generated when empty
	Client         *http.Client  // nil → default tunnel client
	MaxBatchSize   int           // 0 → DefaultMaxBatchSize
	MaxWait        time.Duration // 0 → DefaultMaxWait
	MaxQueuedBytes int           // Write blocks past this; 0 → DefaultMaxQueuedBytes

	// EventsPath overrides the SSE downstream endpoint path (default "/events").
	// Used by DialConnect to point at "/connect".
	EventsPath string
	// UpPath overrides the POST upstream endpoint path (default "/up").
	// Used by DialConnect to point at "/connect-up".
	UpPath string

	// DisableCaps is a test-only knob (cycle-2 plan decision 10): when
	// true the agent sends no X-SSET-Caps request header and ignores the
	// server's response caps — pure cycle-1 wire behavior.
	DisableCaps bool

	// Concurrency is the wanted upstream POST sender depth; 0 → 1
	// (serial cycle-1). Negotiated down to the server's advertisement
	// (see caps.go); >1 arms the sender pool.
	Concurrency int
	// Compress wants gzip-per-batch encoding (decision 5); used only
	// when negotiated on a windowed (concurrency>1) session.
	Compress bool

	// Token is the bearer token for agent authentication
	Token string

	// RequestModifier, when set, takes precedence over Token. It is called
	// before each HTTP request to allow dynamic auth header injection (e.g.,
	// reading a session token from a file that may be refreshed).
	RequestModifier func(*http.Request)

	// OnTokenUpgrade is called when the server returns a new persistent
	// token via X-SSET-Token (PIN redemption). The agent uses the new
	// token for subsequent connections.
	OnTokenUpgrade func(newToken string)

	// AgentID is the human-readable identifier for this agent (e.g. "mydevbox").
	// Sent as X-SSET-Agent-ID header so the server can route connections.
	AgentID string

	// WantTargetHeader tells the server to write the target address as the
	// first line on each yamux stream (for dynamic target mode).
	WantTargetHeader bool

	// Target is the dynamic target address (e.g. "127.0.0.1:22"). When set,
	// it is passed as a query parameter to the server for connect clients.
	// The server validates it against agent config and writes it as the
	// target header on the yamux stream if the agent wants it.
	Target string
}

// Conn is the agent side of the tunnel: a net.Conn over SSE-down +
// batched-POST-up (plan decisions 1+4). It owns both goroutines; every
// goroutine hangs off a context owned by the conn (decision 9).
type Conn struct {
	client    *http.Client
	ownClient bool // Close must reap the transport's idle conns
	upURL     string
	id        string
	token     atomic.Value // string: bearer token, updated via OnTokenUpgrade
	modifier  func(*http.Request)
	seq       atomic.Uint64 // next X-SSET-Seq; serial sender keeps order

	ctx    context.Context
	cancel context.CancelFunc

	batcher *Batcher // Write → batches → POST sender(s)
	down    *Pipe    // SSE events → Read

	// Sender pool (cycle-2 plan decisions 1-2): nil unless concurrency>1
	// was negotiated. submit (batcher's run goroutine only) assigns seqs;
	// workers POST {seq, body} pairs and never touch c.seq.
	gzip   bool        // negotiated gzip-per-batch
	pool   chan upTask // bounded: full channel = backpressure to batcher
	poolWg sync.WaitGroup

	sendErrMu sync.Mutex
	sendErr   error // sticky first pool POST failure (decision 1)

	writeMu       sync.Mutex
	writeDeadline time.Time

	closeOnce sync.Once
	wg        sync.WaitGroup // readLoop
}

// upTask is one seq-numbered batch handed to a pool worker.
type upTask struct {
	seq  uint64
	body []byte
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
		// Plan decision 9: 8 idle conns cover 4 POST workers + 1 SSE +
		// reconnect overlap + spare; no compression (SSE must not be
		// gzip-buffered by the transport).
		client = &http.Client{Transport: &http.Transport{
			MaxIdleConnsPerHost: 8,
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
	conc := cfg.Concurrency
	if conc <= 0 {
		conc = 1
	}

	eventsPath := cfg.EventsPath
	if eventsPath == "" {
		eventsPath = "/events"
	}
	upPath := cfg.UpPath
	if upPath == "" {
		upPath = "/up"
	}
	// Prepend base path to all endpoints.
	basePath := strings.TrimRight(cfg.BasePath, "/")
	eventsPath = basePath + eventsPath
	upPath = basePath + upPath

	c := &Conn{
		client:   client,
		upURL:    cfg.URL + upPath,
		id:       id,
		down:     NewPipe(downPipeCap),
		modifier: cfg.RequestModifier,
	}
	c.token.Store(cfg.Token)
	c.ownClient = cfg.Client == nil
	c.ctx, c.cancel = context.WithCancel(ctx)

	// Build events URL with query parameters.
	eventsURL := cfg.URL + eventsPath + "?id=" + url.QueryEscape(id)
	if cfg.AgentID != "" {
		eventsURL += "&agent=" + url.QueryEscape(cfg.AgentID)
	}
	if cfg.Target != "" {
		eventsURL += "&target=" + url.QueryEscape(cfg.Target)
	}
	if cfg.WantTargetHeader {
		eventsURL += "&want_target=true"
	}

	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, eventsURL, nil)
	if err != nil {
		c.cancel()
		return nil, fmt.Errorf("build events request: %w", err)
	}
	if c.modifier != nil {
		c.modifier(req)
	} else if tok, _ := c.token.Load().(string); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	want := Caps{Concurrency: conc, Batch: maxSize, Gzip: cfg.Compress}
	if !cfg.DisableCaps {
		// The response (with the server's caps) comes after the request,
		// so the agent sends its WANTED set here; both sides compute the
		// per-axis min independently (caps.go contract, decision 3).
		req.Header.Set("X-SSET-Caps", want.String())
	}
	// Agent ID and target header capability negotiation.
	if cfg.AgentID != "" {
		req.Header.Set("X-SSET-Agent-ID", cfg.AgentID)
	}
	if cfg.WantTargetHeader {
		req.Header.Set("X-SSET-Target", "true")
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
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("%w: %s", ErrUnauthorized, resp.Status)
		}
		return nil, fmt.Errorf("open events stream: status %s", resp.Status)
	}
	if upgraded := resp.Header.Get("X-SSET-Token"); upgraded != "" && cfg.OnTokenUpgrade != nil {
		c.token.Store(upgraded)
		cfg.OnTokenUpgrade(upgraded)
	}

	// Negotiate against the server's advertisement; absent/malformed caps
	// (or DisableCaps) fail closed to the zero set = cycle-1 serial.
	var negotiated Caps
	if !cfg.DisableCaps {
		negotiated = NegotiateCaps(want, ParseCaps(resp.Header.Get("X-SSET-Caps")))
	}
	if negotiated.Batch > 0 && negotiated.Batch < maxSize {
		maxSize = negotiated.Batch // negotiation clamps the ceiling (decision 7)
	}
	// gzip rides the windowed upstream protocol: the server 400s the
	// flag on a non-windowed session (caps.go contract point 4).
	c.gzip = negotiated.Gzip && negotiated.Concurrency > 1
	if negotiated.Concurrency > 1 {
		// Sender pool (decision 1): submit blocks on the bounded channel
		// when saturated, so the batcher's busy flag keeps meaning
		// "saturated" and eager flush/coalescing behave exactly as at N=1.
		c.pool = make(chan upTask, negotiated.Concurrency)
		for i := 0; i < negotiated.Concurrency; i++ {
			c.poolWg.Add(1)
			go c.worker()
		}
		c.batcher = NewBatcher(maxSize, maxWait, cfg.MaxQueuedBytes, c.submit)
	} else {
		// N=1: the cycle-1 serial sender, unchanged.
		c.batcher = NewBatcher(maxSize, maxWait, cfg.MaxQueuedBytes, c.send)
	}
	c.wg.Add(1)
	go c.readLoop(resp.Body)
	return c, nil
}

// DialConnect opens an HTTP transport connection for connect clients,
// using the /connect (SSE-down) and /connect-up (POST-up) endpoints.
// It requests concurrency=4 but the server currently does not advertise
// caps on /connect (connect-up has no reorder window), so negotiation
// fails closed to serial POSTs. The request is future-proof for when
// connect-up gains reordering. The agent ID and target are passed as
// query parameters so the server can route to the correct agent.
func DialConnect(ctx context.Context, cfg Config) (*Conn, error) {
	cfg.EventsPath = "/connect"
	cfg.UpPath = "/connect-up"
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4 // request concurrent POSTs; server clamps via negotiation
	}
	// Target is passed as a query parameter; the server determines whether
	// to write it as a target header on the yamux stream based on the
	// agent's session capabilities.
	return DialAgent(ctx, cfg)
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

// send is the batcher's flush func at N=1: one serial POST per batch
// (decision 1). The write deadline maps to the POST request context
// (decision 8). Any POST failure kills the conn (decision 3).
func (c *Conn) send(batch []byte) error {
	return c.post(c.seq.Add(1)-1, batch)
}

// submit is the batcher's flush func with the sender pool (decision 2:
// seq order == byte order because only the batcher's single run goroutine
// calls this). A full pool channel blocks — that backpressure is what
// keeps the batcher's busy/coalescing semantics exact — but Close always
// unblocks it.
func (c *Conn) submit(batch []byte) error {
	t := upTask{seq: c.seq.Add(1) - 1, body: batch}
	select {
	case c.pool <- t:
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}

// worker POSTs tasks in an infinite loop until the pool channel closes.
// A POST failure is sticky (post records it) and kills the conn.
func (c *Conn) worker() {
	defer c.poolWg.Done()
	for t := range c.pool {
		if err := c.post(t.seq, t.body); err != nil {
			return
		}
	}
}

// fail records the first POST error synchronously — before Close starts
// tearing down — so a concurrent Write can never observe the teardown
// without the root error (decision 1's sticky sendErr, at any pool size).
// Errors caused by our own cancel (Close already in progress) are not
// recorded: a user-initiated Close keeps its clean semantics.
func (c *Conn) fail(err error) {
	if c.ctx.Err() != nil {
		return
	}
	c.sendErrMu.Lock()
	if c.sendErr == nil {
		c.sendErr = err
	}
	c.sendErrMu.Unlock()
	go c.Close()
}

// stickyErr returns the recorded POST failure, if any.
func (c *Conn) stickyErr() error {
	c.sendErrMu.Lock()
	defer c.sendErrMu.Unlock()
	return c.sendErr
}

// post sends one upstream batch with the given seq. The write deadline
// maps to the POST request context (decision 8). Any POST failure kills
// the conn (decision 3). With negotiated gzip, a batch goes out
// compressed only when that is strictly smaller (decision 5).
func (c *Conn) post(seq uint64, batch []byte) error {
	ctx := c.ctx
	c.writeMu.Lock()
	dl := c.writeDeadline
	c.writeMu.Unlock()
	if !dl.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}
	body := batch
	var flags string
	if c.gzip {
		if z := gzipBatch(batch); z != nil {
			body, flags = z, "gzip"
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.upURL, bytes.NewReader(body))
	if err != nil {
		c.fail(err)
		return fmt.Errorf("build upstream post: %w", err)
	}
	req.Header.Set("X-SSET-Session", c.id)
	req.Header.Set("X-SSET-Seq", strconv.FormatUint(seq, 10))
	if c.modifier != nil {
		c.modifier(req)
	} else if tok, _ := c.token.Load().(string); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if flags != "" {
		req.Header.Set("X-SSET-Flags", flags)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.fail(err)
		return fmt.Errorf("post upstream batch: %w", err)
	}
	io.Copy(io.Discard, resp.Body) // always drain (decision 7)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("post upstream batch: status %s", resp.Status)
		c.fail(err)
		return err
	}
	return nil
}

// gzipBatch returns the gzip.BestSpeed encoding of b, or nil when
// compression does not shrink it (decision 5: incompressible batches pay
// only the attempt, ≤1% by construction).
func gzipBatch(b []byte) []byte {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil
	}
	if _, err := zw.Write(b); err != nil {
		return nil
	}
	if err := zw.Close(); err != nil {
		return nil
	}
	if buf.Len() >= len(b) {
		return nil
	}
	return buf.Bytes()
}

// Read returns downstream bytes from the SSE stream.
func (c *Conn) Read(b []byte) (int, error) { return c.down.Read(b) }

// Write buffers b for the next upstream POST. A sticky POST failure
// surfaces here (decision 3: POST failure = session death) — the
// batcher's flush error first, then the pool's async send error. If a
// failure-driven Close won the race and closed the batcher first, the
// root error still wins over ErrBatcherClosed.
func (c *Conn) Write(b []byte) (int, error) {
	if err := c.batcher.Err(); err != nil {
		return 0, err
	}
	if err := c.stickyErr(); err != nil {
		return 0, err
	}
	n, err := c.batcher.Write(b)
	if errors.Is(err, ErrBatcherClosed) {
		if ferr := c.batcher.Err(); ferr != nil {
			return 0, ferr
		}
		if ferr := c.stickyErr(); ferr != nil {
			return 0, ferr
		}
	}
	return n, err
}

// Close cancels the conn context first — in-flight and queued POSTs
// abort immediately, so a peer that accepts a POST but never responds
// cannot hang Close (dropped bytes at Close are consistent with the
// fail-fast model, plan decision 3/9). Then it stops the read pipe,
// drains the batcher, closes the pool channel and reaps the workers, and
// waits for the read goroutine. Idempotent.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		c.down.CloseWithError(net.ErrClosed)
		c.batcher.Close()
		if c.pool != nil {
			close(c.pool) // workers drain remaining tasks (posts fail fast on the canceled ctx)
			c.poolWg.Wait()
		}
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
