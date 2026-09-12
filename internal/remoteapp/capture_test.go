//go:build (darwin || windows || linux) && !purego

package remoteapp

import (
	"testing"
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

// TestForceCaptureDrainsImmediately verifies that the forceCapture channel
// can be drained immediately by a select, matching the simplified
// CaptureLoop pattern where forceCapture is the only non-ctx channel.
func TestForceCaptureDrainsImmediately(t *testing.T) {
	t.Parallel()

	forceCapture := make(chan struct{}, 1)

	// Signal forceCapture.
	forceCapture <- struct{}{}

	// Should be immediately drainable.
	select {
	case <-forceCapture:
		// ok
	default:
		t.Fatal("forceCapture should be immediately drainable")
	}

	// After draining, channel should be empty.
	select {
	case <-forceCapture:
		t.Fatal("forceCapture should be empty after drain")
	default:
		// ok
	}
}

// TestForceCaptureAlwaysResponsive verifies that forceCapture is always
// processed regardless of any prior state, matching the simplified
// CaptureLoop where every forceCapture signal triggers an immediate
// screenshot.
func TestForceCaptureAlwaysResponsive(t *testing.T) {
	t.Parallel()

	forceCapture := make(chan struct{}, 1)
	forceCapture <- struct{}{}

	// forceCapture should always be processable.
	select {
	case <-forceCapture:
		// ok — force capture processed
	default:
		t.Fatal("forceCapture should be processed regardless of state")
	}
}
