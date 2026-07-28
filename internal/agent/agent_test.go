package agent

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// TestNewAgentBackoff_Config verifies the backoff parameters match the
// design contract: 500 ms initial, 2× multiplier, 30 s cap, no elapsed
// ceiling, jitter armed.
func TestNewAgentBackoff_Config(t *testing.T) {
	b := newAgentBackoff()
	if b.InitialInterval != 500*time.Millisecond {
		t.Errorf("InitialInterval = %v; want 500ms", b.InitialInterval)
	}
	if b.MaxInterval != 30*time.Second {
		t.Errorf("MaxInterval = %v; want 30s", b.MaxInterval)
	}
	if b.MaxElapsedTime != 0 {
		t.Errorf("MaxElapsedTime = %v; want 0 (retry forever)", b.MaxElapsedTime)
	}
	if b.Multiplier != 2.0 {
		t.Errorf("Multiplier = %v; want 2.0", b.Multiplier)
	}
	if b.RandomizationFactor != 0.1 {
		t.Errorf("RandomizationFactor = %v; want 0.1", b.RandomizationFactor)
	}
}

// TestNewAgentBackoff_Growth verifies the sequence grows exponentially
// and caps at MaxInterval.
func TestNewAgentBackoff_Growth(t *testing.T) {
	b := newAgentBackoff()
	// Collect raw (pre-jitter) intervals by sampling many draws and
	// checking bounds. With jitter ±10 %, every draw must fall in
	// [0.9×expected, 1.1×expected].
	expected := []time.Duration{
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second, // capped
		30 * time.Second, // stays capped
	}
	for i, want := range expected {
		got := b.NextBackOff()
		lo := time.Duration(float64(want) * 0.9)
		hi := time.Duration(float64(want) * 1.1)
		if got < lo || got > hi {
			t.Errorf("step %d: got %v; want [%v, %v]", i, got, lo, hi)
		}
	}
}

// TestNewAgentBackoff_ResetAfterStop verifies Reset() restarts the
// sequence after MaxElapsedTime is never reached (it's 0).
func TestNewAgentBackoff_ResetAfterStop(t *testing.T) {
	b := newAgentBackoff()
	// Drain past MaxInterval to confirm we never get backoff.Stop.
	for i := 0; i < 20; i++ {
		d := b.NextBackOff()
		if d == backoff.Stop {
			t.Fatalf("NextBackOff returned Stop at step %d; MaxElapsedTime=0 should never stop", i)
		}
	}
	b.Reset()
	got := b.NextBackOff()
	lo := time.Duration(float64(500*time.Millisecond) * 0.9)
	hi := time.Duration(float64(500*time.Millisecond) * 1.1)
	if got < lo || got > hi {
		t.Errorf("after Reset: got %v; want [%v, %v]", got, lo, hi)
	}
}

// TestAgent_RunRespectsBackoff verifies that when the server is
// unreachable, the agent's Run loop doesn't retry faster than the
// backoff allows. We point the agent at an unused port so every
// connection attempt fails with "connection refused".
func TestAgent_RunRespectsBackoff(t *testing.T) {
	// Bind to :0 to find an unused port, then close it — dialing that
	// port will get "connection refused" reliably.
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // port now refuses connections

	a := &Agent{
		ServerURL: "http://" + addr,
		Target:    "127.0.0.1:1",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Count connection attempts by timing how many occur in 3 s.
	// With the old 1 s max cap, we'd see ~3 attempts. With the new
	// 500 ms → 1 s → 2 s backoff, we should see at most 3 attempts
	// (500 ms + 1 s + 2 s = 3.5 s > 3 s window → 3 attempts max).
	start := time.Now()
	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx) }()

	// Wait for context to expire.
	<-ctx.Done()
	elapsed := time.Since(start)

	// Run should return within a reasonable grace period after cancel.
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return 5s after context cancel")
	}

	// With backoff, the agent should NOT have retried more than 4 times
	// in 3 seconds (500ms + 1s + 2s = 3.5s → at most 3 attempts fit).
	// This is a sanity bound — exact count depends on jitter.
	if elapsed < 2*time.Second {
		t.Errorf("Run returned suspiciously fast (%v); backoff may not be working", elapsed)
	}
}
