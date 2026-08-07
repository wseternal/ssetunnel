//go:build darwin && purego

package remoteapp

import "strings"

// robotgoCaptureErrSubstr is the error substring from robotgo.CaptureImg when
// the display server cannot produce a screenshot. Pinned to robotgo v0.100.x;
// verify when upgrading.
const robotgoCaptureErrSubstr = "Capture image not found"

// isDisplayUnavailable returns true when the error indicates the display is
// not available for screen capture (e.g. monitor off, no active session).
// On darwin with -tags purego (no CGO), it relies on error-string matching
// since CGDisplayIsActive is unavailable without CoreGraphics CGO.
func isDisplayUnavailable(err error) bool {
	return err != nil && strings.Contains(err.Error(), robotgoCaptureErrSubstr)
}
