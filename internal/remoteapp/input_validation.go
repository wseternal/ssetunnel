package remoteapp

import "strings"

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

// clampCoords clamps x, y to the valid screen range [0, w) / [0, h).
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

// ValidateKeyEvent checks whether a key name and optional modifiers are in
// the whitelist. Returns an error if the key or any modifier is invalid.
func ValidateKeyEvent(key string, mods []string) error {
	k := strings.ToLower(key)
	if !validKeys[k] {
		return &InvalidKeyError{Key: key}
	}
	for _, m := range mods {
		lm := strings.ToLower(m)
		valid2 := map[string]bool{
			"ctrl": true, "control": true,
			"shift": true,
			"alt": true, "option": true,
			"cmd": true, "command": true, "super": true,
		}
		if !valid2[lm] {
			return &InvalidModifierError{Modifier: m}
		}
	}
	return nil
}

// ValidateText checks that text is within the length limit and contains
// no control characters.
func ValidateText(text string) error {
	if len(text) > 256 {
		return &TextTooLongError{Length: len(text)}
	}
	for _, r := range text {
		if r < 0x20 || r == 0x7f {
			return &ControlCharError{Rune: r}
		}
	}
	return nil
}

// ValidateKeyToggleState checks that the state is "down" or "up".
func ValidateKeyToggleState(state string) error {
	if state != "down" && state != "up" {
		return &InvalidStateError{State: state}
	}
	return nil
}

// ValidateScrollAmount clamps the scroll amount to [1, 20].
func ValidateScrollAmount(amount int) int {
	if amount <= 0 {
		return 3
	}
	if amount > 20 {
		return 20
	}
	return amount
}

// ValidateScrollDirection returns the direction if valid, or "down" as default.
func ValidateScrollDirection(dir string) string {
	switch dir {
	case "up", "down", "left", "right":
		return dir
	default:
		return "down"
	}
}

// Input validation error types for testability.

// InvalidKeyError is returned when a key name is not in the whitelist.
type InvalidKeyError struct{ Key string }

func (e *InvalidKeyError) Error() string { return "unknown key: " + e.Key }

// InvalidModifierError is returned when a modifier is not recognized.
type InvalidModifierError struct{ Modifier string }

func (e *InvalidModifierError) Error() string { return "unknown modifier: " + e.Modifier }

// TextTooLongError is returned when type_text exceeds the length limit.
type TextTooLongError struct{ Length int }

func (e *TextTooLongError) Error() string { return "text too long" }

// ControlCharError is returned when type_text contains a control character.
type ControlCharError struct{ Rune rune }

func (e *ControlCharError) Error() string { return "control character rejected" }

// InvalidStateError is returned when key_toggle state is invalid.
type InvalidStateError struct{ State string }

func (e *InvalidStateError) Error() string { return "invalid key_toggle state: " + e.State }
