//go:build darwin && !purego

package remoteapp

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>
*/
import "C"
import "strings"

// isDisplayUnavailable returns true when the primary display is not available
// for screen capture (e.g. monitor is off or sleeping). On macOS, it queries
// CoreGraphics directly: CGDisplayCreateImage returns NULL for inactive
// displays, which is the root cause of the robotgo "Capture image not found."
// error. Falls back to error-string matching if the CGO check is inconclusive.
func isDisplayUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if C.CGDisplayIsActive(C.CGMainDisplayID()) == 0 {
		return true
	}
	// Fallback: robotgo reports "Capture image not found." when the display
	// server cannot produce a screenshot (display sleeping, locked, etc.).
	return strings.Contains(err.Error(), robotgoCaptureErrSubstr)
}

// robotgoCaptureErrSubstr is the error substring from robotgo.CaptureImg when
// the display server cannot produce a screenshot. Pinned to robotgo v0.100.x;
// verify when upgrading.
const robotgoCaptureErrSubstr = "Capture image not found"
