package server

import (
	"context"
	"io"
	"net"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/agent"
	"github.com/wseternal/ssetunnel/internal/testutil"
	"github.com/wseternal/ssetunnel/internal/transport"
)

// dialThroughMiddlebox builds agent conn → middlebox → real handlers.
func dialThroughMiddlebox(t *testing.T, heartbeat time.Duration, cfg testutil.MiddleboxConfig) (*transport.Conn, *Session, *testutil.Middlebox) {
	t.Helper()
	reg := NewRegistry()
	ts := httptest.NewServer(NewHandler(reg, heartbeat))
	t.Cleanup(ts.Close)
	mb, err := testutil.StartMiddlebox(ts.URL, cfg)
	if err != nil {
		t.Fatalf("StartMiddlebox: %v", err)
	}
	t.Cleanup(mb.Close)
	c, err := transport.DialAgent(context.Background(), transport.Config{
		URL:       mb.URL,
		SessionID: "mb-test",
		MaxWait:   time.Millisecond,
	})
	if err != nil {
		t.Fatalf("DialAgent through middlebox: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	var sess *Session
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sess = reg.Get("mb-test"); sess != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if sess == nil {
		t.Fatal("session never registered")
	}
	return c, sess, mb
}

func TestMiddleboxSSESurvivesIdleKiller(t *testing.T) {
	t.Parallel()
	const idleKill = 300 * time.Millisecond
	// 4:1 ratio: 75 ms heartbeats vs 300 ms idle killer (plan step 8).
	c, sess, mb := dialThroughMiddlebox(t, 75*time.Millisecond, testutil.MiddleboxConfig{
		IdleTimeout: idleKill,
	})
	// Survive 3x the idle-kill interval, then prove the stream is live.
	time.Sleep(3 * idleKill)
	if _, err := sess.Write([]byte("alive")); err != nil {
		t.Fatalf("server Write: %v", err)
	}
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("Read after 3x idle-kill interval: %v", err)
	}
	if string(buf) != "alive" {
		t.Fatalf("read %q, want %q", buf, "alive")
	}
	if n := mb.Killed.Load(); n != 0 {
		t.Fatalf("middlebox killed %d conns despite heartbeats, want 0", n)
	}
}

func TestMiddleboxIdleKillerControl(t *testing.T) {
	t.Parallel()
	// Control case: heartbeats off, the same killer must kill the SSE GET.
	c, _, mb := dialThroughMiddlebox(t, time.Hour, testutil.MiddleboxConfig{
		IdleTimeout: 100 * time.Millisecond,
	})
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("Read succeeded: idle killer did not kill the heartbeat-less SSE stream")
	}
	if n := mb.Killed.Load(); n == 0 {
		t.Fatal("middlebox reports 0 kills")
	}
}

func TestMiddleboxBulkNever413(t *testing.T) {
	t.Parallel()
	c, sess, mb := dialThroughMiddlebox(t, time.Hour, testutil.MiddleboxConfig{
		MaxBody: 32 << 10, // above the 16 KiB batch ceiling
	})
	var received atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32<<10)
		for {
			n, err := sess.Read(buf)
			received.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()
	const total = 2 << 20
	// Non-16KiB-aligned, yamux-window-sized writes: without enqueue-time
	// fragmentation the batcher would emit >32 KiB batches → 413.
	chunk := make([]byte, 50000)
	var want int64
	for i := 0; i*len(chunk) < total; i++ {
		if _, err := c.Write(chunk); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		want += int64(len(chunk))
	}
	// Wait for delivery BEFORE Close: cancel-first Close (plan decision
	// 9) drops bytes still buffered at Close.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && received.Load() < want {
		time.Sleep(time.Millisecond)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	sess.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	<-done
	if received.Load() != want {
		t.Fatalf("server received %d bytes, want %d", received.Load(), want)
	}
	if n := mb.Resp413s.Load(); n != 0 {
		t.Fatalf("bulk transfer tripped the body cap %d times, want 0", n)
	}
	if n := mb.Posts.Load(); n == 0 {
		t.Fatal("middlebox saw 0 POSTs, transfer did not go through it")
	}
}

func TestMiddleboxReconnectAfterKill(t *testing.T) {
	t.Parallel()
	// Echo target for the agent.
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go io.Copy(c, c)
		}
	}()

	// Heartbeat (1s) slower than the idle killer (200ms): the middlebox
	// kills the SSE GET; the agent must reconnect with a fresh session.
	srv := NewServer(time.Second)
	ts := httptest.NewServer(srv.HTTPHandler())
	defer ts.Close()
	mb, err := testutil.StartMiddlebox(ts.URL, testutil.MiddleboxConfig{IdleTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("StartMiddlebox: %v", err)
	}
	defer mb.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ag := &agent.Agent{ServerURL: mb.URL, Target: target.Addr().String(), MaxBackoff: 50 * time.Millisecond}
	go ag.Run(ctx)

	seen := map[string]bool{}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, id := range srv.Reg.IDs() {
			seen[id] = true
		}
		if len(seen) >= 2 {
			return // reconnect after killed SSE succeeded
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("saw %d distinct sessions, want >=2 (no reconnect after idle kill)", len(seen))
}
