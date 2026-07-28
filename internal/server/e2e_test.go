package server

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/agent"
)

// e2eEnv is a full in-process deployment: echo target, tunnel server
// with entry listener, and a reconnecting agent.
type e2eEnv struct {
	srv       *Server
	target    net.Listener
	entryAddr string
	cancel    context.CancelFunc
}

func setupE2E(t *testing.T) *e2eEnv {
	t.Helper()
	return setupE2ETuned(t, nil)
}

// setupE2ETuned is setupE2E with a hook to tune the agent's config
// (cycle-2: batch size / concurrency / compression).
func setupE2ETuned(t *testing.T, tune func(*agent.Agent)) *e2eEnv {
	t.Helper()
	// Echo target behind the agent.
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go io.Copy(c, c)
		}
	}()

	srv := NewServer(20 * time.Millisecond)
	ts := httptest.NewServer(srv.HTTPHandler())
	entryLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen entry: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.ServeEntry(ctx, entryLn)

	ag := &agent.Agent{
		ServerURL:  ts.URL,
		Target:     target.Addr().String(),
		MaxBackoff: 50 * time.Millisecond, // fast reconnect for tests
	}
	if tune != nil {
		tune(ag)
	}
	go ag.Run(ctx)

	t.Cleanup(func() {
		cancel()
		entryLn.Close()
		ts.Close()
		target.Close()
	})
	return &e2eEnv{srv: srv, target: target, entryAddr: entryLn.Addr().String(), cancel: cancel}
}

// waitSession polls until a live session other than notID is registered.
func (e *e2eEnv) waitSession(t *testing.T, notID string, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, id := range e.srv.Reg.IDs() {
			if id != notID {
				return id
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no new session within %v", within)
	return ""
}

func (e *e2eEnv) dialEntry(t *testing.T) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", e.entryAddr)
	if err != nil {
		t.Fatalf("dial entry: %v", err)
	}
	c.SetDeadline(time.Now().Add(30 * time.Second)) // generous
	return c
}

// roundTrip writes want through the tunnel and asserts a byte-exact echo.
func roundTrip(t *testing.T, c net.Conn, want []byte) {
	t.Helper()
	go func() {
		c.Write(want)
	}()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("echo not byte-exact")
	}
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i>>8)
	}
	return b
}

func TestE2EEchoByteExact(t *testing.T) {
	t.Parallel()
	e := setupE2E(t)
	e.waitSession(t, "", 5*time.Second)
	c := e.dialEntry(t)
	defer c.Close()
	roundTrip(t, c, pattern(1<<20)) // 1 MiB through entry→tunnel→target
}

// TestE2EEchoByteExactConcurrent: 1 MiB byte-exact echo with the agent
// negotiated at 4 senders / 64 KiB batches / gzip through the real
// server+entry path (cycle-2 plan step 7).
func TestE2EEchoByteExactConcurrent(t *testing.T) {
	t.Parallel()
	e := setupE2ETuned(t, func(ag *agent.Agent) {
		ag.BatchSize = 64 << 10
		ag.Concurrency = 4
		ag.Compress = true
	})
	e.waitSession(t, "", 5*time.Second)
	c := e.dialEntry(t)
	defer c.Close()
	roundTrip(t, c, pattern(1<<20))
}

func TestE2ETwoConcurrent(t *testing.T) {
	t.Parallel()
	e := setupE2E(t)
	e.waitSession(t, "", 5*time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(fill int) {
			defer wg.Done()
			c := e.dialEntry(t)
			defer c.Close()
			roundTrip(t, c, bytes.Repeat([]byte{byte(fill)}, 256<<10))
		}(i + 1)
	}
	wg.Wait()
}

func TestE2EReconnect(t *testing.T) {
	t.Parallel()
	e := setupE2E(t)
	id1 := e.waitSession(t, "", 5*time.Second)

	// Tunnel works.
	probe := e.dialEntry(t)
	roundTrip(t, probe, []byte("before kill"))
	probe.Close()

	// A second entry conn sits idle (stream open, nothing written).
	idle := e.dialEntry(t)
	defer idle.Close()

	// Kill the SSE stream mid-test: the session dies, the agent must
	// reconnect, and the entry side must get a clean close — no hang.
	start := time.Now()
	e.srv.Reg.Get(id1).Close()
	idle.SetReadDeadline(start.Add(5 * time.Second))
	if _, err := idle.Read(make([]byte, 1)); err == nil {
		t.Fatal("idle entry conn still readable after session kill, want clean error")
	}

	// Agent reconnects well under the 5 s budget.
	id2 := e.waitSession(t, id1, 5*time.Second)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("reconnect took %v, budget is 5s", elapsed)
	}
	t.Logf("reconnected as session %s in %v", id2, time.Since(start))

	// New entry conns work again, byte-exact.
	c := e.dialEntry(t)
	defer c.Close()
	roundTrip(t, c, pattern(64<<10))
}
