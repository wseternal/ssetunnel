//go:build remoteapp

package remoteapp

import (
	"fmt"
	"log"
	"strings"

	"github.com/go-vgo/robotgo"
)

// validKeys is a whitelist of recognized robotgo key names.
// Keys not in this set are rejected to prevent unexpected behavior.
var validKeys = map[string]bool{
	// Letters
	"a": true, "b": true, "c": true, "d": true, "e": true,
	"f": true, "g": true, "h": true, "i": true, "j": true,
	"k": true, "l": true, "m": true, "n": true, "o": true,
	"p": true, "q": true, "r": true, "s": true, "t": true,
	"u": true, "v": true, "w": true, "x": true, "y": true, "z": true,
	// Numbers
	"0": true, "1": true, "2": true, "3": true, "4": true,
	"5": true, "6": true, "7": true, "8": true, "9": true,
	// Function keys
	"f1": true, "f2": true, "f3": true, "f4": true, "f5": true, "f6": true,
	"f7": true, "f8": true, "f9": true, "f10": true, "f11": true, "f12": true,
	// Navigation
	"enter": true, "tab": true, "escape": true, "space": true, "backspace": true,
	"delete": true, "up": true, "down": true, "left": true, "right": true,
	"home": true, "end": true, "pageup": true, "pagedown": true, "insert": true,
	// Modifiers
	"ctrl": true, "shift": true, "alt": true, "cmd": true, "super": true,
	"control": true, "option": true, "command": true,
	// Punctuation
	".": true, ",": true, "/": true, "\\": true, ";": true, "'": true,
	"[": true, "]": true, "-": true, "=": true, "`": true,
}

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
		amt := event.Amount
		if amt <= 0 {
			amt = 3
		}
		dir := event.Direction
		switch dir {
		case "up", "down", "left", "right":
			// valid
		default:
			dir = "down"
		}
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
		if !validKeys[key] {
			return fmt.Errorf("unknown key: %q", event.Key)
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
			return fmt.Errorf("unknown key: %q", event.Key)
		}
		state := event.State
		if state == "" {
			state = "down"
		}
		robotgo.KeyToggle(key, state)

	case "type_text":
		if event.Text != "" {
			robotgo.Type(event.Text)
		}

	default:
		log.Printf("remoteapp: unknown input event type: %s", event.Type)
	}
	return nil
}

// clampCoords clamps x, y to the valid screen range.
func clampCoords(x, y, w, h int) (int, int) {
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if w > 0 && x >= w {
		x = w - 1
	}
	if h > 0 && y >= h {
		y = h - 1
	}
	return x, y
}

// mapButton maps a button name to robotgo's expected value.
func mapButton(btn string) string {
	switch btn {
	case "right":
		return "right"
	case "middle":
		return "center"
	default:
		return "left"
	}
}

// sanitizeModifiers filters modifier keys to valid robotgo values.
func sanitizeModifiers(mods []string) []string {
	valid := map[string]bool{
		"ctrl": true, "control": true,
		"shift": true,
		"alt": true, "option": true,
		"cmd": true, "command": true, "super": true,
	}
	var out []string
	for _, m := range mods {
		if valid[strings.ToLower(m)] {
			out = append(out, strings.ToLower(m))
		}
	}
	return out
}
