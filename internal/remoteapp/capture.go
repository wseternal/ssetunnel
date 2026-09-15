//go:build (darwin || windows || linux) && !purego

package remoteapp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/deepteams/webp"
	"github.com/go-vgo/robotgo"
)

// webpQuality controls the WebP encoding quality (0–100).
// 75 provides crisp text rendering at ~80–250 KB per 1080p frame.
const webpQuality = 75

// streamInterval is the minimum time between streaming frames (5 FPS cap).
const streamInterval = 200 * time.Millisecond

// captureImg is a test seam: production code calls robotgo.CaptureImg,
// tests substitute a synthetic image source.
var captureImg = robotgo.CaptureImg

// checkScreenAccessFn is a test seam for the pre-flight permission check.
// Production code calls checkScreenAccess (platform-specific); tests may
// substitute a no-op to bypass the macOS permission gate.
var checkScreenAccessFn = checkScreenAccess

// maxConsecutiveCaptureFails is the number of consecutive capture failures
// before the loop gives up and returns an error (circuit breaker).
const maxConsecutiveCaptureFails = 10

// maxConsecutiveEncodeFails is the number of consecutive WebP encode failures
// before the loop gives up and returns an error (independent of capture).
const maxConsecutiveEncodeFails = 10

// defaultEncoderOpts is the shared EncoderOptions used for every WebP encode.
// Hoisted to avoid allocating a new struct on every frame.
var defaultEncoderOpts = &webp.EncoderOptions{Quality: webpQuality, Method: 4}

// CaptureLoop captures the primary display and writes WebP-encoded
// screenshots as typed frames to w. It runs until ctx is canceled or w
// returns an error. Log events are also written to w for observability.
//
// Capture strategy: an initial screenshot is taken on startup so the
// frontend receives the first frame immediately. After that, screenshots
// are taken when the forceCapture channel is signaled (manual refresh
// from the command palette) or when streaming mode is active (ticker at
// 5 FPS driven by the streaming channel).
//
// The streaming channel toggles continuous capture: true starts a ticker
// at streamInterval (200 ms, 5 FPS hard cap); false stops it. The ticker
// is created and destroyed inside the capture goroutine — no extra
// application-level goroutines or mutexes are needed.
//
// The forceCapture channel triggers an immediate capture when signaled.
// During streaming, force signals are coalesced with the tick schedule:
// a force signal triggers a capture only if ≥streamInterval elapsed since
// the last sent frame; otherwise it is dropped. When NOT streaming, force
// capture behaves as before (immediate).
//
// If w is a *lockedWriter (as used by ProxyRemoteApp), all frame and log
// writes are mutex-guarded for concurrent safety.
func CaptureLoop(ctx context.Context, w io.Writer, forceCapture <-chan struct{}, streaming <-chan bool) error {
	// Detect lockedWriter for mutex-guarded writes.
	lw, _ := w.(*lockedWriter)

	// Reuse buffer across frames to avoid ~150 KB/frame allocation.
	var buf bytes.Buffer
	captureFails := 0
	encodeFails := 0
	var lastFrameAt time.Time // tracks last frame send time for rate limiting

	writeLog := func(severity, message string) {
		if lw != nil {
			_ = lw.writeLogEvent(severity, message)
		} else {
			_ = WriteLogEvent(w, severity, message)
		}
	}
	writeScreenshot := func(webpData []byte) error {
		if lw != nil {
			return lw.writeScreenshotWithTimestamp(webpData, time.Now())
		}
		return WriteScreenshotWithTimestamp(w, webpData, time.Now())
	}

	// captureAndSend captures a screenshot and writes it as a WebP frame.
	// Returns (isTransient=true, err=nil) when capture fails due to the
	// display being unavailable (monitor off/sleeping) — the caller may
	// log the event without tripping the circuit breaker.
	// Returns (false, err) when the circuit breaker trips or a non-transient
	// write/encode error occurs.
	captureAndSend := func() (bool, error) {
		captured, err := captureImg()
		if err != nil {
			if isDisplayUnavailable(err) {
				captureFails = 0 // display-off invalidates prior fail history
				log.Printf("remoteapp: capture: display unavailable: %v", err)
				writeLog("warn", fmt.Sprintf("display unavailable (refresh to retry): %v", err))
				return true, nil
			}
			captureFails++
			if captureFails >= maxConsecutiveCaptureFails {
				writeLog("error", fmt.Sprintf("capture circuit breaker: %d consecutive failures: %v", captureFails, err))
				return false, fmt.Errorf("capture failed %d consecutive times: %w", captureFails, err)
			}
			log.Printf("remoteapp: capture: %v (attempt %d/%d)", err, captureFails, maxConsecutiveCaptureFails)
			writeLog("warn", fmt.Sprintf("capture failed (attempt %d/%d): %v", captureFails, maxConsecutiveCaptureFails, err))
			return false, nil // non-fatal
		}
		captureFails = 0 // reset on success

		buf.Reset()
		if err := webp.Encode(&buf, captured, defaultEncoderOpts); err != nil {
			encodeFails++
			if encodeFails >= maxConsecutiveEncodeFails {
				writeLog("error", fmt.Sprintf("webp encode circuit breaker: %d consecutive failures: %v", encodeFails, err))
				return false, fmt.Errorf("webp encode failed %d consecutive times: %w", encodeFails, err)
			}
			log.Printf("remoteapp: webp encode: %v (attempt %d/%d)", err, encodeFails, maxConsecutiveEncodeFails)
			writeLog("warn", fmt.Sprintf("webp encode failed (attempt %d/%d): %v", encodeFails, maxConsecutiveEncodeFails, err))
			return false, nil // non-fatal
		}
		encodeFails = 0 // reset on success

		if err := writeScreenshot(buf.Bytes()); err != nil {
			return false, err
		}
		lastFrameAt = time.Now()
		return false, nil
	}

	// maybeCapture triggers a capture, respecting the streaming rate cap.
	// When force=true and streaming is active, the capture is skipped if
	// less than streamInterval has elapsed since the last frame (coalesce
	// force-refresh into the tick schedule). When not streaming, force
	// captures immediately.
	maybeCapture := func(force bool, isStreaming bool) error {
		if force && isStreaming && !lastFrameAt.IsZero() && time.Since(lastFrameAt) < streamInterval {
			return nil // coalesced: streaming tick will deliver the next frame
		}
		_, err := captureAndSend()
		return err
	}

	// Pre-flight: verify screen recording permission on platforms that
	// support the check. On macOS (CGO) this calls CGPreflightScreenCaptureAccess;
	// on other platforms the hook is a no-op.
	if err := checkScreenAccessFn(); err != nil {
		writeLog("error", err.Error())
		return err
	}

	writeLog("info", "capture started (manual refresh only)")

	// Initial capture on startup so the frontend receives the first frame.
	if transient, err := captureAndSend(); err != nil {
		return err
	} else if transient {
		writeLog("info", "display unavailable at startup, will retry on next manual refresh")
	}

	// isStreaming tracks the current streaming state.
	// ticker is non-nil only while streaming.
	isStreaming := false
	var ticker *time.Ticker
	var tickC <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			if ticker != nil {
				ticker.Stop()
			}
			writeLog("info", "capture stopped (context canceled)")
			return ctx.Err()
		case on, ok := <-streaming:
			if !ok {
				// Channel closed: stop any active ticker before disabling the case.
				if ticker != nil {
					ticker.Stop()
					ticker = nil
					tickC = nil
				}
				isStreaming = false
				streaming = nil // disable select case
				continue
			}
			if on == isStreaming {
				continue // no state change
			}
			isStreaming = on
			if isStreaming {
				ticker = time.NewTicker(streamInterval)
				tickC = ticker.C
				writeLog("info", "streaming started (5 FPS cap)")
			} else {
				if ticker != nil {
					ticker.Stop()
					ticker = nil
					tickC = nil
				}
				writeLog("info", "streaming stopped")
			}
		case <-tickC:
			if err := maybeCapture(false, true); err != nil {
				return err
			}
		case <-forceCapture:
			writeLog("info", "force capture requested")
			if err := maybeCapture(true, isStreaming); err != nil {
				return err
			}
		}
	}
}

// GetScreenSize returns the primary display dimensions.
func GetScreenSize() (width, height int) {
	return robotgo.GetScreenSize()
}

// Enabled reports whether the remote app feature is compiled in.
func Enabled() bool { return true }
