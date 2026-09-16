//go:build darwin && !purego

package remoteapp

/*
#cgo LDFLAGS: -framework CoreGraphics -framework ApplicationServices
#include <CoreGraphics/CoreGraphics.h>
#include <ApplicationServices/ApplicationServices.h>
*/
import "C"
import (
	"fmt"
	"log"
	"strings"
)

// checkScreenAccess returns an error if the process lacks macOS Screen
// Recording permission. Uses CGPreflightScreenCaptureAccess (macOS 10.15+).
// Returns nil when permission is granted.
func checkScreenAccess() error {
	if !C.CGPreflightScreenCaptureAccess() {
		return fmt.Errorf(
			"screen recording permission denied: grant access in " +
				"System Settings → Privacy & Security → Screen Recording, " +
				"then restart the agent",
		)
	}
	return nil
}

// isDisplayUnavailable returns true when the primary display is not available
// for screen capture (e.g. monitor is off or sleeping). On macOS, it queries
// CoreGraphics directly: CGDisplayCreateImage returns NULL for inactive
// displays, which is the root cause of the robotgo "Capture image not found."
// error.
//
// When the display IS active but capture still fails, it checks screen
// recording permission via CGPreflightScreenCaptureAccess. If permission is
// denied, it returns false so the caller treats the error as permanent
// (not transient).
func isDisplayUnavailable(err error) bool {
	if err == nil {
		return false
	}
	dispID := C.CGMainDisplayID()
	active := C.CGDisplayIsActive(dispID)
	// Display inactive (sleeping/off) → transient.
	if active == 0 {
		log.Printf("remoteapp: isDisplayUnavailable: display_id=%d active=false (display sleeping/off)", dispID)
		return true
	}
	// Display is active: if permission is denied, this is NOT a transient
	// display-unavailable condition — caller should treat as permanent.
	hasPermission := C.CGPreflightScreenCaptureAccess()
	if !hasPermission {
		log.Printf("remoteapp: isDisplayUnavailable: display_id=%d active=true permission=false (treating as permanent failure)", dispID)
		log.Printf("remoteapp: screen recording permission denied (treating as permanent failure)")
		return false
	}
	// Permission granted but capture still failed — genuinely transient
	// (e.g. display mode switching, resolution change).
	isRobotgoErr := strings.Contains(err.Error(), robotgoCaptureErrSubstr)
	log.Printf("remoteapp: isDisplayUnavailable: display_id=%d active=true permission=true error=%q matches_robotgo=%v",
		dispID, err.Error(), isRobotgoErr)
	return isRobotgoErr
}

// robotgoCaptureErrSubstr is the error substring from robotgo.CaptureImg when
// the display server cannot produce a screenshot. Pinned to robotgo v0.100.x;
// verify when upgrading.
const robotgoCaptureErrSubstr = "Capture image not found"
