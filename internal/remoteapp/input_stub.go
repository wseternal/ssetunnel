//go:build !remoteapp

package remoteapp

// DispatchInput dispatches an input event via robotgo.
// This stub returns ErrNotSupported.
func DispatchInput(event InputEvent, screenWidth, screenHeight int) error {
	return ErrNotSupported
}
