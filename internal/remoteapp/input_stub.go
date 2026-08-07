//go:build !(darwin || windows || linux) || purego

package remoteapp

// DispatchInput dispatches an input event via robotgo.
// This stub returns ErrNotSupported.
func DispatchInput(event InputEvent, screenWidth, screenHeight int) error {
	return ErrNotSupported
}

// ReleaseAllInputs releases all potentially held keys and mouse buttons.
// This stub is a no-op.
func ReleaseAllInputs() {}
