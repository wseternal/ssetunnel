package transport_test

import (
	"bytes"
	"context"
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
