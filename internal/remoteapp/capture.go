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

// CaptureLoop captures the primary display at fps frames per second and
// writes JPEG-encoded screenshots as typed frames to w. It runs until
// ctx is canceled or w returns an error. Log events are also written to w
// for console observability.
func CaptureLoop(ctx context.Context, w io.Writer, fps int) error {
	if fps <= 0 {
		fps = 3
	}
	interval := time.Second / time.Duration(fps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Reuse buffer across frames to avoid ~150 KB/frame allocation.
	var buf bytes.Buffer
	consecutiveFails := 0

	_ = WriteLogEvent(w, "info", fmt.Sprintf("capture started at %d FPS", fps))

	for {
		select {
		case <-ctx.Done():
			_ = WriteLogEvent(w, "info", "capture stopped (context canceled)")
			return ctx.Err()
		case <-ticker.C:
			img, err := robotgo.CaptureImg()
			if err != nil {
				consecutiveFails++
				if consecutiveFails >= maxConsecutiveCaptureFails {
					_ = WriteLogEvent(w, "error", fmt.Sprintf("capture circuit breaker: %d consecutive failures: %v", consecutiveFails, err))
					return fmt.Errorf("capture failed %d consecutive times: %w", consecutiveFails, err)
				}
				log.Printf("remoteapp: capture: %v (attempt %d/%d)", err, consecutiveFails, maxConsecutiveCaptureFails)
				_ = WriteLogEvent(w, "warn", fmt.Sprintf("capture failed (attempt %d/%d): %v", consecutiveFails, maxConsecutiveCaptureFails, err))
				continue
			}
			consecutiveFails = 0 // reset on success

			buf.Reset()
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
				consecutiveFails++
				if consecutiveFails >= maxConsecutiveCaptureFails {
					_ = WriteLogEvent(w, "error", fmt.Sprintf("jpeg encode circuit breaker: %d consecutive failures: %v", consecutiveFails, err))
					return fmt.Errorf("jpeg encode failed %d consecutive times: %w", consecutiveFails, err)
				}
				log.Printf("remoteapp: jpeg encode: %v (attempt %d/%d)", err, consecutiveFails, maxConsecutiveCaptureFails)
				_ = WriteLogEvent(w, "warn", fmt.Sprintf("jpeg encode failed (attempt %d/%d): %v", consecutiveFails, maxConsecutiveCaptureFails, err))
				continue
			}
			if err := WriteFrame(w, FrameScreenshot, buf.Bytes()); err != nil {
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
