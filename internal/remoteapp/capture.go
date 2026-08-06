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

// idleTimeout is the fallback capture interval when no action events are
// received. If only mouse_move events arrive (which don't change the screen),
// the capture loop suppresses action-driven captures. This timer ensures at
// least one screenshot is sent every idleTimeout to keep the frontend updated.
const idleTimeout = 15 * time.Second

// CaptureLoop captures the primary display and writes JPEG-encoded
// screenshots as typed frames to w. It runs until ctx is canceled or w
// returns an error. Log events are also written to w for observability.
//
// Capture is triggered by two signals:
//   - captureNow channel: immediate capture on action events (clicks, keys)
//   - 15-second idle timer: fallback when no action events are received
//
// Mouse-move events do NOT trigger captures (they don't change the screen).
// When a captureNow signal fires, the idle timer is reset so captures don't
// double-fire.
//
// If w is a *lockedWriter (as used by ProxyRemoteApp), all frame and log
// writes are mutex-guarded for concurrent safety.
func CaptureLoop(ctx context.Context, w io.Writer, captureNow <-chan struct{}) error {
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

	// drainTimer drains the timer channel if it has a pending fire,
	// preventing a double-capture when an immediate signal arrives.
	drainTimer := func(t *time.Timer) {
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
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

	writeLog("info", "capture started (signal-driven, 15s idle fallback)")

	// Initial capture on startup.
	if err := captureAndSend(); err != nil {
		return err
	}

	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			writeLog("info", "capture stopped (context canceled)")
			return ctx.Err()
		case <-captureNow:
			// Immediate capture on action event: drain idle timer,
			// capture now, then reset idle timer.
			drainTimer(idleTimer)
			if err := captureAndSend(); err != nil {
				return err
			}
			idleTimer.Reset(idleTimeout)
		case <-idleTimer.C:
			// Idle fallback: capture to keep frontend updated when
			// no action events have been received for idleTimeout.
			if err := captureAndSend(); err != nil {
				return err
			}
			idleTimer.Reset(idleTimeout)
		}
	}
}

// GetScreenSize returns the primary display dimensions.
func GetScreenSize() (width, height int) {
	return robotgo.GetScreenSize()
}

// Enabled reports whether the remote app feature is compiled in.
func Enabled() bool { return true }
