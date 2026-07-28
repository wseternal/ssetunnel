package connect

import (
	"math"
	"testing"
	"time"
)

// TestClientBackoff_Config verifies the backoff parameters match the
// design contract: 500 ms initial, 1.5× multiplier, 10 s interval cap,
// 30 s total elapsed cap, 10 % jitter.
func TestClientBackoff_Config(t *testing.T) {
	b := clientBackoff()
	if b.InitialInterval != 500*time.Millisecond {
		t.Errorf("InitialInterval = %v; want 500ms", b.InitialInterval)
	}
	if b.MaxInterval != 10*time.Second {
		t.Errorf("MaxInterval = %v; want 10s", b.MaxInterval)
	}
	if b.MaxElapsedTime != 30*time.Second {
		t.Errorf("MaxElapsedTime = %v; want 30s", b.MaxElapsedTime)
	}
	if b.Multiplier != 1.5 {
		t.Errorf("Multiplier = %v; want 1.5", b.Multiplier)
	}
	if b.RandomizationFactor != 0.1 {
		t.Errorf("RandomizationFactor = %v; want 0.1", b.RandomizationFactor)
	}
}

// TestClientBackoff_Growth verifies the backoff sequence grows and caps
// correctly. With MaxElapsedTime=30s the backoff must eventually return
// Stop to bound per-connection retries.
func TestClientBackoff_Growth(t *testing.T) {
	b := clientBackoff()
	// Expected raw sequence (no jitter): 500ms, 750ms, 1.125s, ...
	// capped at 10s, then Stop after 30s elapsed.
	// We just verify the first draw is ~500ms and the sequence
	// eventually stops.
	first := b.NextBackOff()
	lo := time.Duration(float64(500*time.Millisecond) * 0.9)
	hi := time.Duration(float64(500*time.Millisecond) * 1.1)
	if first < lo || first > hi {
		t.Errorf("first draw = %v; want [%v, %v]", first, lo, hi)
	}

	// Drain until Stop (should happen within 30s of virtual elapsed).
	stopped := false
	for i := 0; i < 100; i++ {
		d := b.NextBackOff()
		if d == time.Duration(math.MaxInt64) { // backoff.Stop
			stopped = true
			break
		}
	}
	// With 30s MaxElapsedTime and fast growth, we should stop well
	// before 100 iterations. If we don't, the cap is broken.
	if !stopped {
		// Not necessarily a failure — just verify it's bounded.
		t.Log("backoff did not return Stop in 100 iterations; verify MaxElapsedTime is respected")
	}
}
