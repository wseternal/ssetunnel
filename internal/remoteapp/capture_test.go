//go:build (darwin || windows || linux) && !purego

package remoteapp

import "testing"

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
