//go:build remoteapp

package remoteapp

import (
	"bytes"
	"context"
	"image/jpeg"
	"io"
	"log"
	"time"

	"github.com/go-vgo/robotgo"
)

// jpegQuality controls the JPEG encoding quality (1–100).
// 50 balances bandwidth (~50–150 KB per 1080p frame) and clarity.
const jpegQuality = 50

// CaptureLoop captures the primary display at fps frames per second and
// writes JPEG-encoded screenshots as typed frames to w. It runs until
// ctx is canceled or w returns an error.
func CaptureLoop(ctx context.Context, w io.Writer, fps int) error {
	if fps <= 0 {
		fps = 3
	}
	interval := time.Second / time.Duration(fps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			img, err := robotgo.CaptureImg()
			if err != nil {
				log.Printf("remoteapp: capture: %v", err)
				continue
			}
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
				log.Printf("remoteapp: jpeg encode: %v", err)
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
