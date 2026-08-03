//go:build !remoteapp

package remoteapp

import (
	"context"
	"io"
)

// CaptureLoop captures screenshots at the specified FPS and writes them
// as typed frames to w. This stub implementation returns ErrNotSupported.
func CaptureLoop(ctx context.Context, w io.Writer, fps int) error {
	return ErrNotSupported
}

// GetScreenSize returns the primary display dimensions.
// This stub returns (0, 0).
func GetScreenSize() (width, height int) {
	return 0, 0
}

// Enabled reports whether the remote app feature is compiled in.
func Enabled() bool { return false }
