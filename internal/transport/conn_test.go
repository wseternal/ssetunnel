package transport_test

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/server"
	"github.com/wseternal/ssetunnel/internal/transport"
)

// setup starts the real step-4 handlers behind httptest.
func setup(t *testing.T, heartbeat time.Duration) (*httptest.Server, *server.Registry) {
	t.Helper()
	reg := server.NewRegistry()
	srv := httptest.NewServer(server.NewHandler(reg, heartbeat))
	t.Cleanup(srv.Close)
	return srv, reg
}

func dial(t *testing.T, srv *httptest.Server, sessionID string) *transport.Conn {
	t.Helper()
	c, err := transport.DialAgent(context.Background(), transport.Config{
		URL:       srv.URL,
		SessionID: sessionID,
		MaxWait:   time.Millisecond,
	})
	if err != nil {
		t.Fatalf("DialAgent: %v", err)
	}
	return c
}

// waitSession polls the registry until the session appears.
func waitSession(t *testing.T, reg *server.Registry, id string) *server.Session {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := reg.Get(id); s != nil {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session %q never registered", id)
	return nil
}

func TestConnEcho(t *testing.T) {
	t.Parallel()
	srv, reg := setup(t, time.Hour)
	c := dial(t, srv, "echo")
	defer c.Close()
	sess := waitSession(t, reg, "echo")
	go io.Copy(sess, sess) // server-side echo, full duplex

	// Client → server → client.
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("Read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q, want %q", buf, "ping")
	}
	// Server → client (unsolicited downstream).
	if _, err := sess.Write([]byte("pong")); err != nil {
		t.Fatalf("server Write: %v", err)
	}
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("Read downstream: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("downstream = %q, want %q", buf, "pong")
	}
}

func TestConnBatchingObserved(t *testing.T) {
	t.Parallel()
	srv, reg := setup(t, time.Hour)
	var posts atomic.Int64
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/up" {
			posts.Add(1)
		}
		srv.Config.Handler.ServeHTTP(w, r)
	})
	counting := httptest.NewServer(wrapped)
	t.Cleanup(counting.Close)

	c := dial(t, counting, "batch")
	sess := waitSession(t, reg, "batch")
	go io.Copy(io.Discard, sess) // drain server side

	const writes = 1000
	for i := 0; i < writes; i++ {
		if _, err := c.Write(bytes.Repeat([]byte{byte(i)}, 100)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Serial POSTs are slow relative to in-memory writes: the sender is
	// busy almost always, so writes coalesce far below one POST per write.
	if n := posts.Load(); n >= writes {
		t.Fatalf("POST count = %d for %d small writes, want batching (<%d)", n, writes, writes)
	} else {
		t.Logf("batching: %d writes → %d POSTs", writes, n)
	}
}

func TestConnCloseWithUnreadData(t *testing.T) {
	t.Parallel()
	srv, reg := setup(t, time.Hour)
	c := dial(t, srv, "closer")
	sess := waitSession(t, reg, "closer")

	// Server pushes 100 KiB downstream; the client never Reads.
	if _, err := sess.Write(bytes.Repeat([]byte{'x'}, 100<<10)); err != nil {
		t.Fatalf("server Write: %v", err)
	}
	// Client pushes upstream; the server never Reads.
	if _, err := c.Write(bytes.Repeat([]byte{'y'}, 100<<10)); err != nil {
		t.Fatalf("client Write: %v", err)
	}
	done := make(chan struct{})
	go func() {
		c.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close hung with unread buffered data")
	}
}

func TestConnGoroutinesSettle(t *testing.T) {
	t.Parallel()
	srv, reg := setup(t, time.Hour)
	before := runtime.NumGoroutine()
	c := dial(t, srv, "settle")
	sess := waitSession(t, reg, "settle")
	go io.Copy(sess, sess)
	if _, err := c.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n := runtime.NumGoroutine(); n <= before+2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("goroutines did not settle: before=%d after=%d", before, runtime.NumGoroutine())
}

func TestConnConcurrentWrite(t *testing.T) {
	t.Parallel()
	srv, reg := setup(t, time.Hour)
	c := dial(t, srv, "conc")
	sess := waitSession(t, reg, "conc")
	var received atomic.Int64
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := sess.Read(buf)
			received.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()

	const writers = 8
	const writesEach = 200
	const chunk = 50
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(fill byte) {
			defer wg.Done()
			b := bytes.Repeat([]byte{fill}, chunk)
			for i := 0; i < writesEach; i++ {
				if _, err := c.Write(b); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}(byte('a' + w))
	}
	wg.Wait()
	// Wait for delivery BEFORE Close: with cancel-first Close (plan
	// decision 9), bytes still buffered at Close are dropped.
	want := int64(writers * writesEach * chunk)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if received.Load() == want {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if received.Load() != want {
		t.Fatalf("server received %d bytes, want %d (lost or duplicated)", received.Load(), want)
	}
}

func TestConnPOSTFailureSurfaces(t *testing.T) {
	t.Parallel()
	reg := server.NewRegistry()
	inner := server.NewHandler(reg, time.Hour)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/up" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(failing.Close)

	c := dial(t, failing, "fail")
	// First POST fails async; a subsequent Write must surface the error.
	var werr error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, werr = c.Write([]byte("x")); werr != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if werr == nil {
		t.Fatal("Write kept succeeding despite POST 500")
	}
	// Conn is dead: Read fails too.
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, rerr := c.Read(make([]byte, 1)); rerr == nil {
		t.Fatal("Read succeeded after POST failure, want conn closed")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestConnHeartbeatFiltered(t *testing.T) {
	t.Parallel()
	srv, reg := setup(t, 5*time.Millisecond) // fast heartbeats
	c := dial(t, srv, "hb")
	defer c.Close()
	sess := waitSession(t, reg, "hb")

	// ~20 heartbeats pass; none may surface as data.
	c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	n, err := c.Read(make([]byte, 16))
	var nerr net.Error
	if n != 0 || !errors.As(err, &nerr) || !nerr.Timeout() {
		t.Fatalf("Read = (%d, %v), want (0, i/o timeout): heartbeat leaked as data", n, err)
	}
	// Real data still arrives.
	if _, err := sess.Write([]byte("hi")); err != nil {
		t.Fatalf("server Write: %v", err)
	}
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf) != "hi" {
		t.Fatalf("read %q, want %q", buf, "hi")
	}
}

func TestConnReadDeadline(t *testing.T) {
	t.Parallel()
	srv, _ := setup(t, time.Hour)
	c := dial(t, srv, "dl")
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	_, err := c.Read(make([]byte, 1))
	var nerr net.Error
	if !errors.As(err, &nerr) || !nerr.Timeout() {
		t.Fatalf("got %v, want net.Error timeout", err)
	}
}

// hangingUpServer serves real /events but accepts POSTs and never
// responds until released — the Critical-1 regression fixture.
func hangingUpServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	reg := server.NewRegistry()
	inner := server.NewHandler(reg, time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/up" {
			io.Copy(io.Discard, r.Body) // accept the body...
			<-release                   // ...but never respond
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // LIFO: runs before srv.Close
	return srv
}

func TestConnCloseHungPOST(t *testing.T) {
	t.Parallel()
	srv := hangingUpServer(t)
	c := dial(t, srv, "hung")
	// Fill the pipe so a POST is in flight against the hung handler.
	if _, err := c.Write(make([]byte, 64<<10)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // let the POST reach the handler
	before := runtime.NumGoroutine()
	done := make(chan struct{})
	go func() {
		c.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close hung on an unanswered POST")
	}
	// Goroutines must settle after the forced teardown.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("goroutines did not settle: before=%d after=%d", before, runtime.NumGoroutine())
}

func TestConnWriteDeadlineExpires(t *testing.T) {
	t.Parallel()
	srv := hangingUpServer(t)
	c := dial(t, srv, "wd")
	defer c.Close()
	c.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
	// The POST hangs; the write deadline must abort it and surface.
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = c.Write([]byte("x")); err != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err == nil {
		t.Fatal("Write kept succeeding against a hung POST past the write deadline")
	}
	if !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("Write error = %v, want a deadline-exceeded timeout", err)
	}
}

// dialCfg dials an agent with the given Config knobs (URL filled in).
func dialCfg(t *testing.T, srv *httptest.Server, cfg transport.Config) *transport.Conn {
	t.Helper()
	cfg.URL = srv.URL
	if cfg.MaxWait == 0 {
		cfg.MaxWait = time.Millisecond
	}
	c, err := transport.DialAgent(context.Background(), cfg)
	if err != nil {
		t.Fatalf("DialAgent: %v", err)
	}
	return c
}

func patternBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i>>8)
	}
	return b
}

// releaseGate parks gated POST handlers until Released; Release is
// idempotent so a cleanup can release all gates without a double close.
type releaseGate struct {
	ch   chan struct{}
	once sync.Once
}

func newReleaseGate() *releaseGate { return &releaseGate{ch: make(chan struct{})} }

func (g *releaseGate) C() <-chan struct{} { return g.ch }

func (g *releaseGate) Release() { g.once.Do(func() { close(g.ch) }) }

// TestConnConcurrentReassembly: 4 sender workers + deterministically
// shuffled POST delivery (release gates, no sleeps) must reassemble
// byte-exact through the server-side reorder window.
func TestConnConcurrentReassembly(t *testing.T) {
	t.Parallel()
	reg := server.NewRegistry()
	h := server.NewHandler(reg, time.Hour)
	gates := make(map[uint64]*releaseGate, 4)
	for i := 0; i < 4; i++ {
		gates[uint64(i)] = newReleaseGate()
	}
	var arrived atomic.Int64
	h.OnUpPush = func(seq uint64) <-chan struct{} {
		arrived.Add(1)
		if g, ok := gates[seq]; ok {
			return g.C()
		}
		return nil
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	// Failure-path safety: never leave a POST handler parked at cleanup.
	t.Cleanup(func() {
		for _, g := range gates {
			g.Release()
		}
	})

	c := dialCfg(t, srv, transport.Config{
		SessionID:    "pool",
		MaxBatchSize: 64 << 10,
		Concurrency:  4,
	})
	defer c.Close()
	sess := waitSession(t, reg, "pool")

	payload := patternBytes(256 << 10) // exactly 4 x 64 KiB batches
	if _, err := c.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// All four POSTs reached the gate (workers in flight, none pushed).
	waitForCond(t, "all POSTs gated", func() bool { return arrived.Load() == 4 })
	// Release in a shuffled order: pushes hit the window out of order.
	for _, seq := range []uint64{2, 0, 3, 1} {
		gates[seq].Release()
	}
	sess.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(sess, got); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("reassembled bytes differ from written payload")
	}
}

// TestConnEagerFlushConcurrent: with the pool idle, a small write must
// flush immediately — the 30 s coalescing ceiling must never apply to
// interactive traffic at concurrency 4 (plan decision 1).
func TestConnEagerFlushConcurrent(t *testing.T) {
	t.Parallel()
	srv, reg := setup(t, time.Hour)
	c := dialCfg(t, srv, transport.Config{
		SessionID:   "eager",
		Concurrency: 4,
		MaxWait:     30 * time.Second, // huge: arrival proves the eager flush
	})
	defer c.Close()
	sess := waitSession(t, reg, "eager")
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	sess.SetReadDeadline(time.Now().Add(3 * time.Second)) // 10x margin
	buf := make([]byte, 4)
	if _, err := io.ReadFull(sess, buf); err != nil {
		t.Fatalf("Read: %v (eager flush lost to coalescing?)", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("read %q, want %q", buf, "ping")
	}
}

// TestConnCoalescingConcurrent: with the pool saturated (4 workers
// parked, bounded channel full), small writes must coalesce into full
// batches instead of trickling one POST per write (plan decision 1).
func TestConnCoalescingConcurrent(t *testing.T) {
	t.Parallel()
	reg := server.NewRegistry()
	h := server.NewHandler(reg, time.Hour)
	park := newReleaseGate()
	var posts atomic.Int64
	h.OnUpPush = func(seq uint64) <-chan struct{} {
		posts.Add(1)
		return park.C()
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Cleanup(park.Release)

	c := dialCfg(t, srv, transport.Config{
		SessionID:    "coal",
		MaxBatchSize: 64 << 10,
		Concurrency:  4,
		MaxWait:      30 * time.Second, // never fires: batching is size-driven here
	})
	defer c.Close()
	sess := waitSession(t, reg, "coal")

	// 8 full batches: 4 workers parked at the gate + 4 in the bounded
	// channel → the next submit blocks → batcher busy → coalescing.
	big := patternBytes(8 * (64 << 10))
	if _, err := c.Write(big); err != nil {
		t.Fatalf("Write big: %v", err)
	}
	waitForCond(t, "pool saturated", func() bool { return posts.Load() == 4 })

	// 65 KiB in 1 KiB writes: the first flushes eagerly (1 KiB), the
	// remaining 64 KiB coalesce into exactly one full batch.
	var small bytes.Buffer
	for i := 0; i < 65; i++ {
		chunk := bytes.Repeat([]byte{byte(i)}, 1<<10)
		small.Write(chunk)
		if _, err := c.Write(chunk); err != nil {
			t.Fatalf("Write small %d: %v", i, err)
		}
	}

	park.Release()
	want := append(big, small.Bytes()...)
	got := make([]byte, len(want))
	sess.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(sess, got); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("delivered bytes differ from written bytes")
	}
	// 8 big + 1 eager 1 KiB + 1 coalesced 64 KiB = 10 POSTs; without
	// coalescing the small writes alone would cost up to 65.
	if n := posts.Load(); n != 10 {
		t.Fatalf("POST count = %d, want 10 (coalescing under saturation)", n)
	}
}

// recordedPost captures one /up request's wire form.
type recordedPost struct {
	flags string
	body  []byte
}

// recordingRT inspects outgoing /up bodies before forwarding.
type recordingRT struct {
	mu    sync.Mutex
	posts []recordedPost
}

func (rt *recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/up" {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		rt.mu.Lock()
		rt.posts = append(rt.posts, recordedPost{req.Header.Get("X-SSET-Flags"), body})
		rt.mu.Unlock()
	}
	return http.DefaultTransport.RoundTrip(req)
}

func (rt *recordingRT) snapshot() []recordedPost {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]recordedPost(nil), rt.posts...)
}

// TestConnGzipWire: gzip is sent only when negotiated, only when smaller
// (plan decision 5), and never on a serial (non-windowed) session.
func TestConnGzipWire(t *testing.T) {
	t.Parallel()
	compressible := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 1500)[:64<<10]
	incompressible := make([]byte, 64<<10)
	if _, err := crand.Read(incompressible); err != nil {
		t.Fatalf("rand: %v", err)
	}
	tests := []struct {
		name        string
		concurrency int
		payload     []byte
		wantFlags   string
		wantSmaller bool // wire body strictly smaller than raw
	}{
		{"compressible negotiated", 4, compressible, "gzip", true},
		{"incompressible negotiated", 4, incompressible, "", false},
		{"compressible serial session", 1, compressible, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, reg := setup(t, time.Hour)
			rt := &recordingRT{}
			c := dialCfg(t, srv, transport.Config{
				SessionID:    "gz",
				Client:       &http.Client{Transport: rt},
				MaxBatchSize: 64 << 10,
				Concurrency:  tt.concurrency,
				Compress:     true,
			})
			defer c.Close()
			sess := waitSession(t, reg, "gz")
			if _, err := c.Write(tt.payload); err != nil {
				t.Fatalf("Write: %v", err)
			}
			// Server decodes byte-exact regardless of wire encoding.
			got := make([]byte, len(tt.payload))
			sess.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.ReadFull(sess, got); err != nil {
				t.Fatalf("Read: %v", err)
			}
			if !bytes.Equal(got, tt.payload) {
				t.Fatal("server-side bytes differ from payload")
			}
			posts := rt.snapshot()
			if len(posts) != 1 {
				t.Fatalf("recorded %d POSTs, want 1", len(posts))
			}
			p := posts[0]
			if p.flags != tt.wantFlags {
				t.Fatalf("X-SSET-Flags = %q, want %q", p.flags, tt.wantFlags)
			}
			if tt.wantSmaller && len(p.body) >= len(tt.payload) {
				t.Fatalf("wire body %d bytes, want < %d (compressible)", len(p.body), len(tt.payload))
			}
			if tt.wantFlags == "" && !bytes.Equal(p.body, tt.payload) {
				t.Fatal("unflagged body is not the raw payload")
			}
		})
	}
}

// TestConnCloseHungPOSTConcurrent: cancel-first Close must not hang even
// with all 4 workers parked in unanswered POSTs (plan decision 9).
func TestConnCloseHungPOSTConcurrent(t *testing.T) {
	t.Parallel()
	srv := hangingUpServer(t)
	c := dialCfg(t, srv, transport.Config{
		SessionID:    "hungp",
		MaxBatchSize: 64 << 10,
		Concurrency:  4,
	})
	// Occupy all 4 workers with POSTs against the hung handler.
	if _, err := c.Write(make([]byte, 4*(64<<10))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // let the POSTs reach the handler
	before := runtime.NumGoroutine()
	done := make(chan struct{})
	go func() {
		c.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close hung with 4 unanswered POSTs in flight")
	}
	// Workers must be reaped after the forced teardown.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("goroutines did not settle: before=%d after=%d", before, runtime.NumGoroutine())
}

// TestConnGoroutinesSettleConcurrent mirrors TestConnGoroutinesSettle
// with the sender pool active: no leaked workers after Close.
func TestConnGoroutinesSettleConcurrent(t *testing.T) {
	t.Parallel()
	srv, reg := setup(t, time.Hour)
	before := runtime.NumGoroutine()
	c := dialCfg(t, srv, transport.Config{SessionID: "settlep", Concurrency: 4})
	sess := waitSession(t, reg, "settlep")
	go io.Copy(sess, sess)
	if _, err := c.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n := runtime.NumGoroutine(); n <= before+2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("goroutines did not settle: before=%d after=%d", before, runtime.NumGoroutine())
}

// waitForCond polls cond with a loose 2s deadline (count-based, not timing).
func waitForCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
