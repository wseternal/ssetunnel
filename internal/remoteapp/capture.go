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

// displayOffBackoff is the retry interval when capture repeatedly fails due
// to the display being unavailable (e.g. monitor off or sleeping). This is
// longer than deferDelay to reduce log noise while remaining responsive
// when the display comes back.
const displayOffBackoff = 30 * time.Second

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

	// drainTimer stops the timer and drains its channel if it already fired.
	// Safe to call regardless of timer state.
	drainTimer := func(t *time.Timer) {
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
	}

	writeLog("info", fmt.Sprintf("capture started (deferred, %v idle)", deferDelay))

	// backoffDeadline tracks the earliest time the next retry is allowed
	// when the display is unavailable. Zero value means no active backoff.
	var backoffDeadline time.Time

	// Initial capture on startup so the frontend receives the first frame.
	if transient, err := captureAndSend(); err != nil {
		return err
	} else if transient {
		writeLog("info", "display unavailable at startup, will retry with backoff")
		backoffDeadline = time.Now().Add(displayOffBackoff)
	}

	initialDelay := deferDelay
	if !backoffDeadline.IsZero() {
		initialDelay = displayOffBackoff
	}
	deferTimer := time.NewTimer(initialDelay)
	defer func() {
		if !deferTimer.Stop() {
			select {
			case <-deferTimer.C:
			default:
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			writeLog("info", "capture stopped (context canceled)")
			return ctx.Err()
		case <-inputReceived:
			// Input event received: defer capture. Stop the running
			// timer and restart it. Capture will fire only after the
			// input stream has been quiet for deferDelay.
			drainTimer(deferTimer)
			backoffDeadline = time.Time{} // cancel any active backoff
			deferTimer.Reset(deferDelay)
		case <-deferTimer.C:
			// Timer fired: check whether we should capture now or
			// wait longer due to display-unavailable backoff.
			if !time.Now().Before(backoffDeadline) {
				writeLog("info", "deferred capture fired")
				transient, err := captureAndSend()
				if err != nil {
					return err
				}
				if transient {
					// Display unavailable: schedule retry with backoff
					// instead of the normal short deferral.
					backoffDeadline = time.Now().Add(displayOffBackoff)
					deferTimer.Reset(displayOffBackoff)
				} else {
					backoffDeadline = time.Time{}
					deferTimer.Reset(deferDelay)
				}
			} else {
				// Still in backoff: re-arm timer for remaining wait.
				remaining := time.Until(backoffDeadline)
				deferTimer.Reset(remaining)
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
