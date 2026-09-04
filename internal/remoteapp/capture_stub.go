//go:build !(darwin || windows || linux) || purego

package remoteapp

import (
	"context"
	"io"
)

// CaptureLoop is the stub for unsupported platforms. Returns ErrNotSupported.
func CaptureLoop(ctx context.Context, w io.Writer, inputReceived <-chan struct{}, forceCapture <-chan struct{}) error {
	return ErrNotSupported
}

// GetScreenSize returns the primary display dimensions.
// This stub returns (0, 0).
func GetScreenSize() (width, height int) {
	return 0, 0
}

// Enabled reports whether the remote app feature is compiled in.
func Enabled() bool { return false }
