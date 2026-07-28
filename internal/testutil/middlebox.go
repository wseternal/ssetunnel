// Package testutil holds test-only helpers: currently the middlebox
// proxy that simulates a hostile corporate proxy (plan step 8).
package testutil

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// MiddleboxConfig parameterizes the hostile behaviors of a Middlebox.
// Zero values disable the behavior.
type MiddleboxConfig struct {
	IdleTimeout time.Duration // close client conns idle this long
	MaxBody     int64         // 413 when Content-Length exceeds this
	Latency     time.Duration // fixed delay injected per direction
}

// Middlebox is an HTTP reverse proxy that simulates a hostile middlebox
// (plan step 8): idle-timeout killer, POST body cap, and latency
// injection. Latency on the response path is pipelined through a
// bounded delay queue, so it costs each chunk a fixed delay without
// capping throughput — like a real network.
type Middlebox struct {
	URL string

	Posts    atomic.Int64 // /up requests forwarded
	Resp413s atomic.Int64 // requests rejected by the body cap
	Killed   atomic.Int64 // client conns closed by the idle killer

	cfg      MiddleboxConfig
	upstream *url.URL
	rt       *http.Transport
	srv      *httptest.Server
	stop     chan struct{}

	mu    sync.Mutex
	conns map[*trackedConn]struct{}
}

// StartMiddlebox proxies all traffic to upstreamURL, applying cfg.
func StartMiddlebox(upstreamURL string, cfg MiddleboxConfig) (*Middlebox, error) {
	up, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream url: %w", err)
	}
	m := &Middlebox{
		cfg:      cfg,
		upstream: up,
		rt: &http.Transport{
			MaxIdleConnsPerHost: 64,
			DisableCompression:  true, // never gzip-buffer the SSE stream
		},
		stop:  make(chan struct{}),
		conns: make(map[*trackedConn]struct{}),
	}
	m.srv = httptest.NewUnstartedServer(m)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	m.srv.Listener = &trackedListener{Listener: ln, m: m}
	m.srv.Start()
	m.URL = m.srv.URL
	if cfg.IdleTimeout > 0 {
		go m.idleKiller()
	}
	return m, nil
}

// Close shuts the proxy down.
func (m *Middlebox) Close() {
	close(m.stop)
	m.srv.Close()
}

// ServeHTTP forwards one request upstream, enforcing the body cap and
// injecting request latency; the response streams back with per-chunk
// flush and pipelined latency.
func (m *Middlebox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m.cfg.MaxBody > 0 && r.ContentLength > m.cfg.MaxBody {
		m.Resp413s.Add(1)
		http.Error(w, "middlebox: body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if r.URL.Path == "/up" {
		m.Posts.Add(1)
	}
	if m.cfg.Latency > 0 {
		time.Sleep(m.cfg.Latency) // request direction
	}
	out := r.Clone(r.Context())
	out.URL.Scheme = m.upstream.Scheme
	out.URL.Host = m.upstream.Host
	out.Host = m.upstream.Host
	out.RequestURI = ""
	resp, err := m.rt.RoundTrip(out)
	if err != nil {
		http.Error(w, "middlebox: upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	h := w.Header()
	for k, vs := range resp.Header {
		switch k {
		case "Connection", "Keep-Alive", "Transfer-Encoding", "Content-Length", "Upgrade":
			continue // hop-by-hop or length-managed
		}
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	f, _ := w.(http.Flusher)
	if f != nil {
		f.Flush() // SSE headers must reach the agent immediately
	}
	if m.cfg.Latency > 0 {
		m.copyDelayed(w, f, resp.Body)
		return
	}
	copyFlushed(w, f, resp.Body)
}

// copyFlushed streams body to w, flushing per chunk.
func copyFlushed(w http.ResponseWriter, f http.Flusher, body io.ReadCloser) {
	buf := make([]byte, 32<<10)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			if f != nil {
				f.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

// copyDelayed streams body to w through a bounded delay queue: every
// chunk is released cfg.Latency after it was read, preserving order.
// Pipelining keeps throughput intact while each chunk pays the delay.
func (m *Middlebox) copyDelayed(w http.ResponseWriter, f http.Flusher, body io.ReadCloser) {
	type delayed struct {
		data []byte
		at   time.Time
	}
	const queueChunks = 128 // 128 × 32 KiB = 4 MiB in flight max
	q := make(chan delayed, queueChunks)
	werr := make(chan error, 1)
	go func() {
		for d := range q {
			if s := time.Until(d.at); s > 0 {
				time.Sleep(s)
			}
			if _, err := w.Write(d.data); err != nil {
				werr <- err
				return
			}
			if f != nil {
				f.Flush()
			}
		}
		werr <- nil
	}()
	buf := make([]byte, 32<<10)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			select {
			case q <- delayed{chunk, time.Now().Add(m.cfg.Latency)}:
			case <-werr:
				return // client gone (e.g. idle killer fired)
			}
		}
		if rerr != nil {
			close(q)
			<-werr
			return
		}
	}
}

// trackedConn records last-activity time for the idle killer.
type trackedConn struct {
	net.Conn
	m    *Middlebox
	last atomic.Int64 // unixnano
}

func (c *trackedConn) touch() { c.last.Store(time.Now().UnixNano()) }

func (c *trackedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func (c *trackedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func (c *trackedConn) Close() error {
	c.m.mu.Lock()
	delete(c.m.conns, c)
	c.m.mu.Unlock()
	return c.Conn.Close()
}

// trackedListener wraps accepted conns for activity tracking.
type trackedListener struct {
	net.Listener
	m *Middlebox
}

func (l *trackedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tc := &trackedConn{Conn: c, m: l.m}
	tc.touch()
	l.m.mu.Lock()
	l.m.conns[tc] = struct{}{}
	l.m.mu.Unlock()
	return tc, nil
}

// idleKiller closes any client conn idle for longer than IdleTimeout,
// like a stateful middlebox reaping "stalled" connections.
func (m *Middlebox) idleKiller() {
	tick := m.cfg.IdleTimeout / 4
	if tick < 5*time.Millisecond {
		tick = 5 * time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-t.C:
			m.mu.Lock()
			for c := range m.conns {
				if now.Sub(time.Unix(0, c.last.Load())) >= m.cfg.IdleTimeout {
					c.Conn.Close() // bypass tracked Close; we hold the lock
					delete(m.conns, c)
					m.Killed.Add(1)
				}
			}
			m.mu.Unlock()
		}
	}
}
