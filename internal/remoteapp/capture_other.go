//go:build !darwin && (windows || linux)

package remoteapp

import "strings"

// robotgoCaptureErrSubstr is the error substring from robotgo.CaptureImg when
// the display server cannot produce a screenshot. Pinned to robotgo v0.100.x;
// verify when upgrading.
const robotgoCaptureErrSubstr = "Capture image not found"

// isDisplayUnavailable returns true when the error indicates the display is
// not available for screen capture (e.g. monitor off, no active session).
// On non-Darwin platforms, it relies on error-string matching since there is
// no portable API to query display state without a full capture attempt.
func isDisplayUnavailable(err error) bool {
	return err != nil && strings.Contains(err.Error(), robotgoCaptureErrSubstr)
}
