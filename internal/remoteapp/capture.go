//go:build darwin || windows || linux

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
// 50 balances bandwidth (~50–150 KB per 1080p frame) and clarity.
const jpegQuality = 50

// maxConsecutiveCaptureFails is the number of consecutive capture failures
// before the loop gives up and returns an error (circuit breaker).
const maxConsecutiveCaptureFails = 10

// deferDelay is the minimum idle period before capturing. While input events
// are flowing, each one resets this timer; capture only fires after the
// stream has been quiet for deferDelay. This avoids uploading screenshots
// that will be immediately stale because more input is in flight.
const deferDelay = 3 * time.Second

// CaptureLoop captures the primary display and writes JPEG-encoded
// screenshots as typed frames to w. It runs until ctx is canceled or w
// returns an error. Log events are also written to w for observability.
//
// Capture uses a deferred-capture strategy: every input event signals the
// inputReceived channel, which resets a deferDelay timer. A screenshot is
// taken only after the timer expires (i.e., no input for deferDelay). This
// avoids uploading screenshots that will be immediately stale while the
// user is actively interacting.
//
// An initial capture is performed on startup so the frontend receives the
// first frame immediately.
//
// If w is a *lockedWriter (as used by ProxyRemoteApp), all frame and log
// writes are mutex-guarded for concurrent safety.
func CaptureLoop(ctx context.Context, w io.Writer, inputReceived <-chan struct{}) error {
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

	captureAndSend := func() error {
		img, err := robotgo.CaptureImg()
		if err != nil {
			consecutiveFails++
			if consecutiveFails >= maxConsecutiveCaptureFails {
				writeLog("error", fmt.Sprintf("capture circuit breaker: %d consecutive failures: %v", consecutiveFails, err))
				return fmt.Errorf("capture failed %d consecutive times: %w", consecutiveFails, err)
			}
			log.Printf("remoteapp: capture: %v (attempt %d/%d)", err, consecutiveFails, maxConsecutiveCaptureFails)
			writeLog("warn", fmt.Sprintf("capture failed (attempt %d/%d): %v", consecutiveFails, maxConsecutiveCaptureFails, err))
			return nil // non-fatal
		}
		consecutiveFails = 0 // reset on success

		buf.Reset()
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
			consecutiveFails++
			if consecutiveFails >= maxConsecutiveCaptureFails {
				writeLog("error", fmt.Sprintf("jpeg encode circuit breaker: %d consecutive failures: %v", consecutiveFails, err))
				return fmt.Errorf("jpeg encode failed %d consecutive times: %w", consecutiveFails, err)
			}
			log.Printf("remoteapp: jpeg encode: %v (attempt %d/%d)", err, consecutiveFails, maxConsecutiveCaptureFails)
			writeLog("warn", fmt.Sprintf("jpeg encode failed (attempt %d/%d): %v", consecutiveFails, maxConsecutiveCaptureFails, err))
			return nil // non-fatal
		}
		return writeScreenshot(buf.Bytes())
	}

	writeLog("info", "capture started (deferred, 3s idle)")

	// Initial capture on startup so the frontend receives the first frame.
	if err := captureAndSend(); err != nil {
		return err
	}

	deferTimer := time.NewTimer(deferDelay)
	defer deferTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			writeLog("info", "capture stopped (context canceled)")
			return ctx.Err()
		case <-inputReceived:
			// Input event received: defer capture. Stop the running
			// timer and restart it. Capture will fire only after the
			// input stream has been quiet for deferDelay.
			if !deferTimer.Stop() {
				select {
				case <-deferTimer.C:
				default:
				}
			}
			deferTimer.Reset(deferDelay)
		case <-deferTimer.C:
			// No input for deferDelay: capture now and restart timer.
			if err := captureAndSend(); err != nil {
				return err
			}
			deferTimer.Reset(deferDelay)
		}
	}
}

// GetScreenSize returns the primary display dimensions.
func GetScreenSize() (width, height int) {
	return robotgo.GetScreenSize()
}

// Enabled reports whether the remote app feature is compiled in.
func Enabled() bool { return true }
