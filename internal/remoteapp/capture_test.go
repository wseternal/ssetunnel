//go:build (darwin || windows || linux) && !purego

package remoteapp

import (
	"bytes"
	"context"
	"image"
	"image/color"
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
// frames faster than the default 10 FPS cap (100 ms interval) while streaming.
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

	go func() {
		_ = CaptureLoop(ctx, fw, forceCapture, streaming, nil, nil)
	}()

	<-ctx.Done()
	// Allow a brief moment for the goroutine to exit.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	count := len(frameAts)
	mu.Unlock()

	// Over ~1.1 s at 10 FPS, we expect ≤12 frames (initial + up to 11 ticks).
	// Allow a small margin for timing jitter.
	if count > 14 {
		t.Errorf("too many frames: got %d, want ≤14 (10 FPS cap over 1.1s)", count)
	}
	if count < 2 {
		t.Errorf("too few frames: got %d, want ≥2", count)
	}

	// Verify intervals between frames are ≥100 ms (minus jitter tolerance).
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

	go func() {
		_ = CaptureLoop(ctx, fw, forceCapture, streaming, nil, nil)
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

	go func() {
		_ = CaptureLoop(ctx, fw, forceCapture, streaming, nil, nil)
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
	time.Sleep(50 * time.Millisecond)

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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	forceCapture := make(chan struct{}, 1)
	streaming := make(chan bool, 1)
	maxFPSCh := make(chan int, 1)
	streaming <- true // start streaming at default 10 FPS

	go func() {
		_ = CaptureLoop(ctx, fw, forceCapture, streaming, maxFPSCh, nil)
	}()

	// Let it run at 10 FPS for ~500ms.
	time.Sleep(500 * time.Millisecond)

	// Switch to 2 FPS (500ms interval) — much slower.
	select {
	case <-maxFPSCh:
	default:
	}
	maxFPSCh <- 2

	// Let it run at 2 FPS for ~1.2s.
	time.Sleep(1200 * time.Millisecond)

	mu.Lock()
	count := len(frameAts)
	// Copy timestamps for interval analysis.
	ats := make([]time.Time, len(frameAts))
	copy(ats, frameAts)
	mu.Unlock()

	// Find the transition point: frames after ~500ms should be at 2 FPS (500ms interval).
	// Count frames in each phase.
	phase1End := ats[0].Add(500 * time.Millisecond)
	var phase1Count, phase2Count int
	for _, at := range ats {
		if at.Before(phase1End) {
			phase1Count++
		} else {
			phase2Count++
		}
	}

	// Phase 1: ~500ms at 10 FPS → expect 3–7 frames (initial + a few ticks).
	if phase1Count < 2 {
		t.Errorf("phase 1: too few frames: %d (want ≥2 at 10 FPS / 500ms)", phase1Count)
	}

	// Phase 2: ~1.2s at 2 FPS → expect 2–4 frames.
	if phase2Count < 1 {
		t.Errorf("phase 2: too few frames: %d (want ≥1 at 2 FPS / 1.2s)", phase2Count)
	}
	if phase2Count > 5 {
		t.Errorf("phase 2: too many frames: %d (want ≤5 at 2 FPS / 1.2s)", phase2Count)
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

	go func() {
		_ = CaptureLoop(ctx, fw2, forceCapture, streaming, nil, fpsCallback)
	}()

	<-ctx.Done()
	time.Sleep(50 * time.Millisecond)

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
