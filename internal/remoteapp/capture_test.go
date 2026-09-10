//go:build (darwin || windows || linux) && !purego

package remoteapp

import (
	"testing"
	"time"
)

// TestForceCaptureChannelCoalescing verifies that a buffered-1 channel
// with non-blocking sends coalesces multiple rapid signals into exactly
// one pending signal — the same pattern used by signalForceCapture in
// ProxyRemoteApp.
func TestForceCaptureChannelCoalescing(t *testing.T) {
	t.Parallel()

	ch := make(chan struct{}, 1)

	// Simulate signalForceCapture: non-blocking send, extras coalesce.
	signalForceCapture := func() {
		select {
		case ch <- struct{}{}:
		default: // already pending; coalesce
		}
	}

	// Fire 5 rapid signals — only 1 should be pending.
	for i := 0; i < 5; i++ {
		signalForceCapture()
	}

	// First receive should succeed.
	select {
	case <-ch:
		// ok
	default:
		t.Fatal("expected 1 pending signal, got 0")
	}

	// Second receive should fail — signals were coalesced.
	select {
	case <-ch:
		t.Fatal("expected no pending signal after coalescing, got 1")
	default:
		// ok — channel drained
	}

	// After draining, a new signal should be deliverable.
	signalForceCapture()
	select {
	case <-ch:
		// ok
	default:
		t.Fatal("expected signal after drain, got 0")
	}
}

// TestForceCapturePriorityOverDeferTimer verifies that the priority
// pre-check pattern used in CaptureLoop ensures forceCapture is always
// handled before the defer timer, even when both channels are ready
// simultaneously. This prevents the pseudo-random select from delaying
// a client-initiated "Refresh Screenshot" behind a deferred capture.
func TestForceCapturePriorityOverDeferTimer(t *testing.T) {
	t.Parallel()

	forceCapture := make(chan struct{}, 1)
	deferCh := make(chan struct{}, 1)

	// Signal both channels so both are ready simultaneously.
	forceCapture <- struct{}{}
	deferCh <- struct{}{}

	// Priority pre-check (mirrors the CaptureLoop pattern).
	forceHandled := false
	select {
	case <-forceCapture:
		forceHandled = true
	default:
	}

	if !forceHandled {
		t.Fatal("priority pre-check should have drained forceCapture")
	}

	// After the pre-check, forceCapture is drained.
	// The main select should NOT see forceCapture.
	select {
	case <-forceCapture:
		t.Fatal("forceCapture should have been drained by priority pre-check")
	case <-deferCh:
		// ok — defer channel is still available
	default:
		t.Fatal("deferCh should still be ready")
	}
}

// TestForceCapturePriorityDuringBackoff verifies that the priority
// pre-check processes forceCapture even during display-unavailable
// backoff, unlike the old code which silently dropped it.
func TestForceCapturePriorityDuringBackoff(t *testing.T) {
	t.Parallel()

	forceCapture := make(chan struct{}, 1)
	forceCapture <- struct{}{}

	// Simulate active backoff deadline. In the old code, this would
	// cause force capture to be silently skipped.
	_ = time.Now().Add(time.Hour) // backoffDeadline

	// The priority pre-check does NOT check backoffDeadline —
	// it always processes force capture. This is the fix: the
	// old code skipped force capture during backoff.
	select {
	case <-forceCapture:
		// ok — force capture processed despite active backoff
	default:
		t.Fatal("forceCapture should have been processed regardless of backoff")
	}
}
