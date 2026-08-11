package server

import (
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
