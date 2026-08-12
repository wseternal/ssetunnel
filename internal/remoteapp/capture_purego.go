//go:build purego

package remoteapp

import "strings"

// robotgoCaptureErrSubstr is the error substring from robotgo.CaptureImg when
// the display server cannot produce a screenshot. Pinned to robotgo v0.100.x;
// verify when upgrading.
const robotgoCaptureErrSubstr = "Capture image not found"

// isDisplayUnavailable returns true when the error indicates the display is
// not available for screen capture (e.g. monitor off, no active session).
// With -tags purego (no CGO), it relies on error-string matching since
// platform-specific display query APIs (e.g. CoreGraphics on darwin) are
// unavailable.
func isDisplayUnavailable(err error) bool {
	return err != nil && strings.Contains(err.Error(), robotgoCaptureErrSubstr)
}

// checkScreenAccess is a no-op with -tags purego (no CGO for platform APIs).
// Always returns nil.
func checkScreenAccess() error { return nil }
