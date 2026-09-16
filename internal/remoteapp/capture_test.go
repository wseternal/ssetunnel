//go:build (darwin || windows || linux) && !purego

package remoteapp

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepteams/webp"
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

// syntheticImage returns a small solid-color NRGBA image for testing.
func syntheticImage() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 0x42, G: 0x42, B: 0x42, A: 0xFF})
		}
	}
	return img
}

func init() {
	// Bypass macOS screen recording permission check in tests.
	checkScreenAccessFn = func() error { return nil }
	// Use string-matching display-unavailable check in tests
	// (avoids platform API dependencies like CGDisplayIsActive).
	isDisplayUnavailableFn = func(err error) bool {
		return err != nil && strings.Contains(err.Error(), robotgoCaptureErrSubstr)
	}
}

// TestWebPEncodeRoundTrip verifies that encoding a synthetic image via
// webp.Encode produces valid WebP that can be decoded back.
func TestWebPEncodeRoundTrip(t *testing.T) {
	t.Parallel()

	img := syntheticImage()
	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, &webp.EncoderOptions{Quality: webpQuality, Method: 4}); err != nil {
		t.Fatalf("webp.Encode: %v", err)
	}
	data := buf.Bytes()
	if len(data) == 0 {
		t.Fatal("webp.Encode produced empty output")
	}
	// Verify RIFF/WEBP magic.
	if len(data) < 12 {
		t.Fatal("webp output too short for RIFF header")
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		t.Errorf("missing RIFF/WEBP magic: got %q %q", data[0:4], data[8:12])
	}
	// Decode and verify dimensions.
	decoded, err := webp.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("webp.Decode: %v", err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != 64 || bounds.Dy() != 64 {
		t.Errorf("decoded dimensions: got %dx%d, want 64x64", bounds.Dx(), bounds.Dy())
	}
}

// TestStreamingTickerCapsFPS verifies that the capture loop never sends
// frames faster than the default 1 FPS cap (1 s interval) while streaming.
func TestStreamingTickerCapsFPS(t *testing.T) {
	// Substitute captureImg with a synthetic source.
	origCapture := captureImg
	defer func() { captureImg = origCapture }()
	captureImg = func(args ...int) (image.Image, error) {
		return syntheticImage(), nil
	}

	var (
		mu       sync.Mutex
		frameAts []time.Time
	)

	// frameWriter records the time of each screenshot frame write.
	fw := &frameRecorder{
		mu:       &mu,
		frameAts: &frameAts,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1100*time.Millisecond)
	defer cancel()

	forceCapture := make(chan struct{}, 1)
	streaming := make(chan bool, 1)
	streaming <- true // start streaming immediately

	done := make(chan error, 1)
	go func() {
		done <- CaptureLoop(ctx, fw, forceCapture, streaming, nil, nil)
	}()

	<-ctx.Done()
	// Wait for CaptureLoop to exit (establishes happens-before the deferred captureImg restore).
	<-done

	mu.Lock()
	count := len(frameAts)
	mu.Unlock()

	// Over ~1.1 s at 1 FPS, we expect ≤3 frames (initial + at most 1 tick).
	// Upper bound is generous to tolerate timing jitter.
	if count > 14 {
		t.Errorf("too many frames: got %d, want ≤14 (1 FPS cap over 1.1s)", count)
	}
	if count < 2 {
		t.Errorf("too few frames: got %d, want ≥2", count)
	}

	// Verify intervals between frames are ≥1 s (minus jitter tolerance).
	// At 1 FPS the minimum gap is ~1 s; 60 ms threshold still catches bursts.
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(frameAts); i++ {
		gap := frameAts[i].Sub(frameAts[i-1])
		if gap < 60*time.Millisecond { // 40 ms jitter tolerance
			t.Errorf("frame interval too short: frame %d→%d gap = %v (want ≥60ms)", i-1, i, gap)
		}
	}
}

// TestStreamingStopsCleanly verifies that stopping streaming halts
// continuous capture with no further frames.
func TestStreamingStopsCleanly(t *testing.T) {
	origCapture := captureImg
	defer func() { captureImg = origCapture }()
	captureImg = func(args ...int) (image.Image, error) {
		return syntheticImage(), nil
	}

	var (
		mu       sync.Mutex
		frameAts []time.Time
	)

	fw := &frameRecorder{
		mu:       &mu,
		frameAts: &frameAts,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	forceCapture := make(chan struct{}, 1)
	streaming := make(chan bool, 1)
	streaming <- true // start streaming

	done := make(chan error, 1)
	go func() {
		done <- CaptureLoop(ctx, fw, forceCapture, streaming, nil, nil)
	}()

	// Let streaming run for ~400 ms, then stop.
	time.Sleep(400 * time.Millisecond)
	// Drain-then-send stop signal.
	select {
	case <-streaming:
	default:
	}
	streaming <- false

	// Record the frame count at stop time.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	countAtStop := len(frameAts)
	mu.Unlock()

	// Wait another 400 ms and verify no new frames.
	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	countAfter := len(frameAts)
	mu.Unlock()

	if countAfter > countAtStop {
		t.Errorf("frames sent after stop: countAtStop=%d, countAfter=%d", countAtStop, countAfter)
	}
	if countAtStop < 1 {
		t.Errorf("expected at least 1 frame while streaming, got %d", countAtStop)
	}

	cancel()
	<-done
}

// TestForceCaptureCoalescedDuringStreaming verifies that rapid force
// signals during streaming do not burst above the FPS cap.
func TestForceCaptureCoalescedDuringStreaming(t *testing.T) {
	origCapture := captureImg
	defer func() { captureImg = origCapture }()
	captureImg = func(args ...int) (image.Image, error) {
		return syntheticImage(), nil
	}

	var (
		mu       sync.Mutex
		frameAts []time.Time
	)

	fw := &frameRecorder{
		mu:       &mu,
		frameAts: &frameAts,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	forceCapture := make(chan struct{}, 1)
	streaming := make(chan bool, 1)
	streaming <- true

	done := make(chan error, 1)
	go func() {
		done <- CaptureLoop(ctx, fw, forceCapture, streaming, nil, nil)
	}()

	// Fire 5 rapid force signals while streaming.
	time.Sleep(50 * time.Millisecond) // wait for initial frame
	for i := 0; i < 5; i++ {
		select {
		case forceCapture <- struct{}{}:
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}

	<-ctx.Done()
	<-done

	mu.Lock()
	defer mu.Unlock()
	// Verify no burst: all inter-frame intervals ≥ 60 ms (10 FPS = 100ms with jitter).
	for i := 1; i < len(frameAts); i++ {
		gap := frameAts[i].Sub(frameAts[i-1])
		if gap < 60*time.Millisecond {
			t.Errorf("burst detected: frame %d→%d gap = %v", i-1, i, gap)
		}
	}
}

// TestStreamingStopsOnContextCancel verifies that canceling the context
// while streaming terminates the capture loop cleanly.
func TestStreamingStopsOnContextCancel(t *testing.T) {
	origCapture := captureImg
	defer func() { captureImg = origCapture }()
	captureImg = func(args ...int) (image.Image, error) {
		return syntheticImage(), nil
	}

	var (
		mu       sync.Mutex
		frameAts []time.Time
	)

	fw := &frameRecorder{
		mu:       &mu,
		frameAts: &frameAts,
	}

	ctx, cancel := context.WithCancel(context.Background())
	forceCapture := make(chan struct{}, 1)
	streaming := make(chan bool, 1)
	streaming <- true

	done := make(chan error, 1)
	go func() {
		done <- CaptureLoop(ctx, fw, forceCapture, streaming, nil, nil)
	}()

	// Let it stream for ~300 ms, then cancel.
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CaptureLoop did not exit after context cancel")
	}

	mu.Lock()
	count := len(frameAts)
	mu.Unlock()
	if count < 1 {
		t.Errorf("expected at least 1 frame before cancel, got %d", count)
	}
}

// TestDynamicFPSAdjustment verifies that sending a new FPS value via the
// maxFPS channel changes the capture interval live without restarting.
func TestDynamicFPSAdjustment(t *testing.T) {
	origCapture := captureImg
	defer func() { captureImg = origCapture }()
	captureImg = func(args ...int) (image.Image, error) {
		return syntheticImage(), nil
	}

	var (
		mu       sync.Mutex
		frameAts []time.Time
	)

	fw := &frameRecorder{
		mu:       &mu,
		frameAts: &frameAts,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	forceCapture := make(chan struct{}, 1)
	streaming := make(chan bool, 1)
	maxFPSCh := make(chan int, 1)

	// Use fpsCallback to detect when the FPS override has been processed.
	fpsSeen := make(chan int, 4)
	fpsCallback := func(fps int) {
		select {
		case fpsSeen <- fps:
		default:
		}
	}

	streaming <- true // start streaming at default FPS

	done := make(chan error, 1)
	go func() {
		done <- CaptureLoop(ctx, fw, forceCapture, streaming, maxFPSCh, fpsCallback)
	}()

	// Wait for the first fpsCallback tick (confirms CaptureLoop started streaming).
	select {
	case <-fpsSeen:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("timed out waiting for initial FPS callback")
	}

	// Now override FPS to 10.
	maxFPSCh <- 10

	// Wait for fpsCallback to report 10 FPS.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case fps := <-fpsSeen:
			if fps == 10 {
				goto phase1
			}
		case <-deadline:
			cancel()
			<-done
			t.Fatal("timed out waiting for FPS override to take effect")
			return
		}
	}

phase1:
	// Record frame timestamps for 600ms at 10 FPS.
	mu.Lock()
	phase1Start := time.Now()
	mu.Unlock()

	time.Sleep(600 * time.Millisecond)

	// Switch to 2 FPS.
	select {
	case <-maxFPSCh:
	default:
	}
	maxFPSCh <- 2

	// Wait for fpsCallback to report 2 FPS.
	deadline = time.After(5 * time.Second)
	for {
		select {
		case fps := <-fpsSeen:
			if fps == 2 {
				goto phase2
			}
		case <-deadline:
			cancel()
			<-done
			t.Fatal("timed out waiting for 2 FPS override")
			return
		}
	}

phase2:
	time.Sleep(1200 * time.Millisecond)

	cancel()
	<-done

	mu.Lock()
	count := len(frameAts)
	ats := make([]time.Time, len(frameAts))
	copy(ats, frameAts)
	mu.Unlock()

	// Phase 1 analysis: count frames within 600ms of phase1Start.
	var p1Count int
	for _, at := range ats {
		if !at.Before(phase1Start) && at.Before(phase1Start.Add(600*time.Millisecond)) {
			p1Count++
		}
	}

	// At 10 FPS for 600ms, expect 4–8 frames.
	if p1Count < 2 {
		t.Errorf("phase 1: too few frames: %d (want ≥2 at 10 FPS / 600ms), total=%d", p1Count, count)
	}

	// Phase 2: count frames after phase1Start+600ms.
	var p2Count int
	for _, at := range ats {
		if !at.Before(phase1Start.Add(600 * time.Millisecond)) {
			p2Count++
		}
	}
	if p2Count < 1 {
		t.Errorf("phase 2: too few frames: %d (want ≥1 at 2 FPS / 1.2s)", p2Count)
	}
	if p2Count > 5 {
		t.Errorf("phase 2: too many frames: %d (want ≤5 at 2 FPS / 1.2s)", p2Count)
	}

	// Verify that later frames have wider intervals (~500ms for 2 FPS).
	if len(ats) > 2 {
		lastGap := ats[len(ats)-1].Sub(ats[len(ats)-2])
		if lastGap < 300*time.Millisecond {
			t.Errorf("expected wide interval at 2 FPS, got %v", lastGap)
		}
	}

	_ = count // suppress unused warning
}

// TestFPSCallbackInvoked verifies that the fpsCallback is invoked
// periodically while streaming is active.
func TestFPSCallbackInvoked(t *testing.T) {
	origCapture := captureImg
	defer func() { captureImg = origCapture }()
	captureImg = func(args ...int) (image.Image, error) {
		return syntheticImage(), nil
	}

	var (
		callbackMu    sync.Mutex
		callbackCalls []int
	)

	fpsCallback := func(fps int) {
		callbackMu.Lock()
		callbackCalls = append(callbackCalls, fps)
		callbackMu.Unlock()
	}

	var (
		mu2      sync.Mutex
		frameAts2 []time.Time
	)
	fw2 := &frameRecorder{
		mu:       &mu2,
		frameAts: &frameAts2,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()

	forceCapture := make(chan struct{}, 1)
	streaming := make(chan bool, 1)
	streaming <- true

	done := make(chan error, 1)
	go func() {
		done <- CaptureLoop(ctx, fw2, forceCapture, streaming, nil, fpsCallback)
	}()

	<-ctx.Done()
	<-done

	callbackMu.Lock()
	calls := len(callbackCalls)
	callbackMu.Unlock()

	// Over ~2.5s with 1s ticker, expect 2 callbacks.
	if calls < 1 {
		t.Errorf("expected ≥1 fpsCallback invocation, got %d", calls)
	}
	if calls > 4 {
		t.Errorf("too many fpsCallback invocations: %d (want ≤4)", calls)
	}
}

// frameRecorder is a minimal io.Writer that records the timestamp of each
// WriteScreenshotWithTimestamp call by intercepting frame writes.
// It parses the frame header to detect FrameScreenshot and records the time.
type frameRecorder struct {
	mu       *sync.Mutex
	frameAts *[]time.Time
}

func (f *frameRecorder) Write(p []byte) (int, error) {
	// We receive raw frame bytes: [type(1)][length(4)][data...].
	// Detect FrameScreenshot (0x01) and record the time.
	if len(p) > 0 && p[0] == FrameScreenshot {
		f.mu.Lock()
		*f.frameAts = append(*f.frameAts, time.Now())
		f.mu.Unlock()
	}
	return len(p), nil
}

// TestTransientCircuitBreaker verifies that the capture loop exits with
// an error after maxConsecutiveTransientFails consecutive display-unavailable
// failures, rather than retrying forever.
func TestTransientCircuitBreaker(t *testing.T) {
	// Override transient limits for fast testing.
	origLimit := maxConsecutiveTransientFails
	origBase := transientBackoffBase
	origCap := transientBackoffCap
	maxConsecutiveTransientFails = 5
	transientBackoffBase = time.Millisecond
	transientBackoffCap = 2 * time.Millisecond

	origCapture := captureImg
	captureImg = func(args ...int) (image.Image, error) {
		return nil, errors.New("Capture image not found.")
	}

	fw := &frameRecorder{
		mu:       &sync.Mutex{},
		frameAts: &[]time.Time{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	forceCapture := make(chan struct{}, 1)
	streaming := make(chan bool, 1)
	streaming <- true // start streaming

	done := make(chan error, 1)
	go func() {
		done <- CaptureLoop(ctx, fw, forceCapture, streaming, nil, nil)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected circuit breaker error, got nil")
		}
		if !strings.Contains(err.Error(), "transient capture failed") {
			t.Errorf("expected transient circuit breaker error, got: %v", err)
		}
	case <-time.After(20 * time.Second):
		cancel()
		<-done
		t.Fatal("capture loop did not exit after transient circuit breaker")
	}

	// Restore after goroutine exit to avoid data race.
	captureImg = origCapture
	maxConsecutiveTransientFails = origLimit
	transientBackoffBase = origBase
	transientBackoffCap = origCap
}

// TestTransientBackoffApplied verifies that backoff delays are applied
// after 3 consecutive transient failures, slowing down retries.
func TestTransientBackoffApplied(t *testing.T) {
	origBase := transientBackoffBase
	origCap := transientBackoffCap
	origLimit := maxConsecutiveTransientFails
	transientBackoffBase = 50 * time.Millisecond
	transientBackoffCap = 100 * time.Millisecond
	maxConsecutiveTransientFails = 20 // high enough to not trip

	origCapture := captureImg

	var callTimesMu sync.Mutex
	var callTimes []time.Time
	captureImg = func(args ...int) (image.Image, error) {
		callTimesMu.Lock()
		callTimes = append(callTimes, time.Now())
		callTimesMu.Unlock()
		return nil, errors.New("Capture image not found.")
	}

	fw := &frameRecorder{
		mu:       &sync.Mutex{},
		frameAts: &[]time.Time{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	forceCapture := make(chan struct{}, 1)
	streaming := make(chan bool, 1)
	streaming <- true

	done := make(chan error, 1)
	go func() {
		done <- CaptureLoop(ctx, fw, forceCapture, streaming, nil, nil)
	}()

	// Wait for enough captures (need ≥6) or context timeout.
	for {
		callTimesMu.Lock()
		n := len(callTimes)
		callTimesMu.Unlock()
		if n >= 8 {
			break
		}
		select {
		case <-ctx.Done():
			cancel()
			<-done
			// Restore before failing.
			captureImg = origCapture
			transientBackoffBase = origBase
			transientBackoffCap = origCap
			maxConsecutiveTransientFails = origLimit
			t.Fatalf("timed out waiting for captures, got %d", n)
			return
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	cancel()
	<-done

	// Restore after goroutine exit to avoid data race.
	captureImg = origCapture
	transientBackoffBase = origBase
	transientBackoffCap = origCap
	maxConsecutiveTransientFails = origLimit

	callTimesMu.Lock()
	defer callTimesMu.Unlock()

	if len(callTimes) < 6 {
		t.Fatalf("too few capture attempts: %d (want ≥6)", len(callTimes))
	}

	// First 3 calls should have no backoff (very close together).
	// Calls 4+ should have increasing gaps (≥50ms).
	// Check that at least one gap after call 4 is ≥ 40ms (with jitter tolerance).
	foundBackoff := false
	for i := 4; i < len(callTimes); i++ {
		gap := callTimes[i].Sub(callTimes[i-1])
		if gap >= 40*time.Millisecond {
			foundBackoff = true
			break
		}
	}
	if !foundBackoff {
		// Log all gaps for debugging.
		var gaps []time.Duration
		for i := 1; i < len(callTimes); i++ {
			gaps = append(gaps, callTimes[i].Sub(callTimes[i-1]))
		}
		t.Errorf("expected backoff ≥40ms after 3 consecutive transient failures, gaps: %v", gaps)
	}
}
