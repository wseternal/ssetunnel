package remoteapp

import (
	"log"
	"strings"

	"github.com/go-vgo/robotgo"
)

// DispatchInput dispatches an input event to the local desktop via robotgo.
// Coordinates are clamped to [0, screenWidth) / [0, screenHeight).
func DispatchInput(event InputEvent, screenWidth, screenHeight int) error {
	switch event.Type {
	case "mouse_move":
		x, y := clampCoords(event.X, event.Y, screenWidth, screenHeight)
		robotgo.Move(x, y)

	case "mouse_click":
		x, y := clampCoords(event.X, event.Y, screenWidth, screenHeight)
		robotgo.Move(x, y)
		btn := mapButton(event.Button)
		robotgo.Click(btn)

	case "mouse_scroll":
		amt := ValidateScrollAmount(event.Amount)
		dir := ValidateScrollDirection(event.Direction)
		robotgo.ScrollDir(amt, dir)

	case "mouse_drag":
		x, y := clampCoords(event.X, event.Y, screenWidth, screenHeight)
		btn := mapButton(event.Button)
		if event.State == "down" {
			robotgo.Toggle(btn, "down")
		}
		robotgo.Move(x, y)
		if event.State == "up" {
			robotgo.Toggle(btn, "up")
		}

	case "key_tap":
		key := strings.ToLower(event.Key)
		if err := ValidateKeyEvent(key, event.Modifiers); err != nil {
			return err
		}
		mods := sanitizeModifiers(event.Modifiers)
		if len(mods) > 0 {
			robotgo.KeyTap(key, mods)
		} else {
			robotgo.KeyTap(key)
		}

	case "key_toggle":
		key := strings.ToLower(event.Key)
		if !validKeys[key] {
			return &InvalidKeyError{Key: event.Key}
		}
		state := event.State
		if state == "" {
			state = "down"
		}
		if err := ValidateKeyToggleState(state); err != nil {
			return err
		}
		robotgo.KeyToggle(key, state)

	case "type_text":
		if err := ValidateText(event.Text); err != nil {
			return err
		}
		if event.Text != "" {
			robotgo.Type(event.Text)
		}

	default:
		log.Printf("remoteapp: unknown input event type: %s", event.Type)
	}
	return nil
}

// ReleaseAllInputs releases all potentially held keys and mouse buttons.
// Called on session teardown to prevent stuck keys from a lost "up" event.
func ReleaseAllInputs() {
	// Release common mouse buttons.
	for _, btn := range []string{"left", "right", "center"} {
		robotgo.Toggle(btn, "up")
	}
	// Release common modifier keys.
	for _, mod := range []string{"ctrl", "shift", "alt", "cmd"} {
		robotgo.KeyToggle(mod, "up")
	}
}
