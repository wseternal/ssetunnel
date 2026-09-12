//go:build (darwin || windows || linux) && !purego

package remoteapp

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"io"
	"log"
	"time"

	"github.com/go-vgo/robotgo"
)

// jpegQuality controls the JPEG encoding quality (1–100).
// 75 provides crisp text rendering at ~100–300 KB per 1080p frame.
const jpegQuality = 75

// maxConsecutiveCaptureFails is the number of consecutive capture failures
// before the loop gives up and returns an error (circuit breaker).
const maxConsecutiveCaptureFails = 10

// CaptureLoop captures the primary display and writes JPEG-encoded
// screenshots as typed frames to w. It runs until ctx is canceled or w
// returns an error. Log events are also written to w for observability.
//
// Capture strategy: an initial screenshot is taken on startup so the
// frontend receives the first frame immediately. After that, screenshots
// are only taken when the forceCapture channel is signaled (manual
// refresh from the command palette). No automatic re-capture occurs.
//
// The forceCapture channel triggers an immediate capture when signaled.
// This supports client-initiated "refresh" actions from the command
// palette.
//
// If w is a *lockedWriter (as used by ProxyRemoteApp), all frame and log
// writes are mutex-guarded for concurrent safety.
func CaptureLoop(ctx context.Context, w io.Writer, forceCapture <-chan struct{}) error {
	// Detect lockedWriter for mutex-guarded writes.
	lw, _ := w.(*lockedWriter)

	// Reuse buffer across frames to avoid ~150 KB/frame allocation.
	var buf bytes.Buffer
	consecutiveFails := 0

	writeLog := func(severity, message string) {
		if lw != nil {
			_ = lw.writeLogEvent(severity, message)
		} else {
			_ = WriteLogEvent(w, severity, message)
		}
	}
	writeScreenshot := func(jpegData []byte) error {
		if lw != nil {
			return lw.writeScreenshotWithTimestamp(jpegData, time.Now())
		}
		return WriteScreenshotWithTimestamp(w, jpegData, time.Now())
	}

	// captureAndSend captures a screenshot and writes it as a JPEG frame.
	// Returns (isTransient=true, err=nil) when capture fails due to the
	// display being unavailable (monitor off/sleeping) — the caller should
	// back off and retry without tripping the circuit breaker.
	// Returns (false, err) when the circuit breaker trips or a non-transient
	// write/encode error occurs.
	captureAndSend := func() (bool, error) {
		img, err := robotgo.CaptureImg()
		if err != nil {
			if isDisplayUnavailable(err) {
				consecutiveFails = 0 // display-off invalidates prior fail history
				log.Printf("remoteapp: capture: display unavailable: %v", err)
				writeLog("warn", fmt.Sprintf("display unavailable (will retry): %v", err))
				return true, nil
			}
			consecutiveFails++
			if consecutiveFails >= maxConsecutiveCaptureFails {
				writeLog("error", fmt.Sprintf("capture circuit breaker: %d consecutive failures: %v", consecutiveFails, err))
				return false, fmt.Errorf("capture failed %d consecutive times: %w", consecutiveFails, err)
			}
			log.Printf("remoteapp: capture: %v (attempt %d/%d)", err, consecutiveFails, maxConsecutiveCaptureFails)
			writeLog("warn", fmt.Sprintf("capture failed (attempt %d/%d): %v", consecutiveFails, maxConsecutiveCaptureFails, err))
			return false, nil // non-fatal
		}
		consecutiveFails = 0 // reset on success

		buf.Reset()
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
			consecutiveFails++
			if consecutiveFails >= maxConsecutiveCaptureFails {
				writeLog("error", fmt.Sprintf("jpeg encode circuit breaker: %d consecutive failures: %v", consecutiveFails, err))
				return false, fmt.Errorf("jpeg encode failed %d consecutive times: %w", consecutiveFails, err)
			}
			log.Printf("remoteapp: jpeg encode: %v (attempt %d/%d)", err, consecutiveFails, maxConsecutiveCaptureFails)
			writeLog("warn", fmt.Sprintf("jpeg encode failed (attempt %d/%d): %v", consecutiveFails, maxConsecutiveCaptureFails, err))
			return false, nil // non-fatal
		}
		return false, writeScreenshot(buf.Bytes())
	}

	// Pre-flight: verify screen recording permission on platforms that
	// support the check. On macOS (CGO) this calls CGPreflightScreenCaptureAccess;
	// on other platforms the hook is a no-op.
	if err := checkScreenAccess(); err != nil {
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

	for {
		select {
		case <-ctx.Done():
			writeLog("info", "capture stopped (context canceled)")
			return ctx.Err()
		case <-forceCapture:
			writeLog("info", "force capture requested")
			if _, err := captureAndSend(); err != nil {
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
