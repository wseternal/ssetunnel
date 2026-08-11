package server

import (
	"bytes"
	"testing"
	"time"
)

func TestShellSessionRegistry_StoreLoadDelete(t *testing.T) {
	reg := NewShellSessionRegistry()

	ss := &ShellSession{
		id:           "test-1",
		agentID:      "agent-a",
		userID:       42,
		ring:         NewRingBuffer(64),
		wakeup:       make(chan struct{}, 1),
		done:         make(chan struct{}),
		createdAt:    time.Now(),
		lastActivity: time.Now(),
	}

	reg.Store(ss)
	if got := reg.Load("test-1"); got != ss {
		t.Fatalf("Load = %v, want %v", got, ss)
	}
	if got := reg.Load("nonexistent"); got != nil {
		t.Fatalf("Load nonexistent = %v, want nil", got)
	}

	reg.Delete("test-1")
	if got := reg.Load("test-1"); got != nil {
		t.Fatalf("after Delete, Load = %v, want nil", got)
	}
}

func TestShellSessionRegistry_FindByUserAgent(t *testing.T) {
	reg := NewShellSessionRegistry()

	ss1 := &ShellSession{
		id: "s1", agentID: "agent-a", userID: 1,
		ring: NewRingBuffer(64), wakeup: make(chan struct{}, 1), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}
	ss2 := &ShellSession{
		id: "s2", agentID: "agent-b", userID: 2,
		ring: NewRingBuffer(64), wakeup: make(chan struct{}, 1), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}
	reg.Store(ss1)
	reg.Store(ss2)

	// User 1 finds agent-a.
	if got := reg.FindByUserAgent(1, "agent-a"); got != ss1 {
		t.Fatalf("FindByUserAgent(1, agent-a) = %v, want ss1", got)
	}
	// User 1 cannot find agent-b.
	if got := reg.FindByUserAgent(1, "agent-b"); got != nil {
		t.Fatalf("FindByUserAgent(1, agent-b) = %v, want nil", got)
	}
	// Admin (userID < 0) finds any.
	if got := reg.FindByUserAgent(-1, "agent-b"); got != ss2 {
		t.Fatalf("FindByUserAgent(-1, agent-b) = %v, want ss2", got)
	}
	// Not found.
	if got := reg.FindByUserAgent(1, "agent-c"); got != nil {
		t.Fatalf("FindByUserAgent(1, agent-c) = %v, want nil", got)
	}
}

func TestShellSessionRegistry_CleanupIdle(t *testing.T) {
	reg := NewShellSessionRegistry()

	ss := &ShellSession{
		id: "idle-1", agentID: "agent-a", userID: 1,
		ring: NewRingBuffer(64), wakeup: make(chan struct{}, 1), done: make(chan struct{}),
		createdAt:    time.Now(),
		lastActivity: time.Now().Add(-2 * time.Hour), // well past timeout
	}
	reg.Store(ss)

	// Cleanup with 1-hour timeout should remove it.
	reg.CleanupIdle(time.Hour)
	if got := reg.Load("idle-1"); got != nil {
		t.Fatalf("after CleanupIdle, Load = %v, want nil", got)
	}
}

func TestShellSessionRegistry_CleanupIdle_SkipsAttached(t *testing.T) {
	reg := NewShellSessionRegistry()

	ss := &ShellSession{
		id: "attached-1", agentID: "agent-a", userID: 1,
		ring: NewRingBuffer(64), wakeup: make(chan struct{}, 1), done: make(chan struct{}),
		createdAt:    time.Now(),
		lastActivity: time.Now().Add(-2 * time.Hour),
	}
	// Simulate attached state.
	ss.sseWriter = &discardWriter{}
	reg.Store(ss)

	// Cleanup should NOT remove attached sessions.
	reg.CleanupIdle(time.Hour)
	if got := reg.Load("attached-1"); got == nil {
		t.Fatalf("CleanupIdle removed attached session, should have skipped")
	}
}

type discardWriter struct{}

func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestShellSessionRegistry_CloseAll(t *testing.T) {
	reg := NewShellSessionRegistry()

	ss := &ShellSession{
		id: "close-all", agentID: "agent-a", userID: 1,
		ring: NewRingBuffer(64), wakeup: make(chan struct{}, 1), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}
	reg.Store(ss)
	reg.CloseAll()

	if got := reg.Load("close-all"); got != nil {
		t.Fatalf("after CloseAll, Load = %v, want nil", got)
	}
	if !ss.IsDead() {
		t.Fatalf("session should be dead after CloseAll")
	}
}

func TestShellSession_AttachClosedSession(t *testing.T) {
	ss := &ShellSession{
		id: "closed-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(64), wakeup: make(chan struct{}, 1), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}
	ss.closed.Store(true)

	if err := ss.Attach(&discardWriter{}, nil); err == nil {
		t.Fatal("Attach on closed session should fail")
	}
}

func TestShellSession_DoubleAttach(t *testing.T) {
	ss := &ShellSession{
		id: "dbl-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(64), wakeup: make(chan struct{}, 1), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}

	if err := ss.Attach(&discardWriter{}, nil); err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	if err := ss.Attach(&discardWriter{}, nil); err == nil {
		t.Fatal("second Attach should fail (already attached)")
	}
	ss.Detach()
	// After Detach, should be able to Attach again.
	if err := ss.Attach(&discardWriter{}, nil); err != nil {
		t.Fatalf("Attach after Detach: %v", err)
	}
}

func TestShellSession_AttachDrainsRingBuffer(t *testing.T) {
	ss := &ShellSession{
		id: "drain-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(256), wakeup: make(chan struct{}, 1), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}

	// Pre-populate the ring buffer.
	ss.ring.Write([]byte("hello world"))
	if ss.ring.Len() != 11 {
		t.Fatalf("ring Len = %d, want 11", ss.ring.Len())
	}

	// Attach should drain the ring buffer.
	var buf bytes.Buffer
	f := &nopFlusher{}
	if err := ss.Attach(&buf, f); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if ss.ring.Len() != 0 {
		t.Fatalf("after Attach, ring Len = %d, want 0", ss.ring.Len())
	}
	if buf.Len() == 0 {
		t.Fatal("Attach should have written buffered data to writer")
	}
}

func TestShellSession_CloseIdempotent(t *testing.T) {
	ss := &ShellSession{
		id: "idem-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(64), wakeup: make(chan struct{}, 1), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}

	if err := ss.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := ss.Close(); err != nil {
		t.Fatalf("second Close should be no-op, got: %v", err)
	}
	if !ss.IsDead() {
		t.Fatal("session should be dead after Close")
	}
}

func TestShellSession_DetachClearsWriter(t *testing.T) {
	ss := &ShellSession{
		id: "det-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(64), wakeup: make(chan struct{}, 1), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}

	ss.Attach(&discardWriter{}, nil)
	if !ss.Attached() {
		t.Fatal("should be attached")
	}
	ss.Detach()
	if ss.Attached() {
		t.Fatal("should not be attached after Detach")
	}
}

type nopFlusher struct{}

func (f *nopFlusher) Flush() {}
