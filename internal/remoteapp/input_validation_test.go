package remoteapp

import (
	"errors"
	"testing"
)

func TestClampCoords(t *testing.T) {
	tests := []struct {
		name         string
		x, y, w, h   int
		wantX, wantY int
		wantOK       bool
	}{
		{"normal", 100, 200, 1920, 1080, 100, 200, true},
		{"negative x", -5, 100, 1920, 1080, 0, 100, true},
		{"negative y", 100, -5, 1920, 1080, 100, 0, true},
		{"overflow x", 1920, 100, 1920, 1080, 1919, 100, true},
		{"overflow y", 100, 1080, 1920, 1080, 100, 1079, true},
		{"zero screen", 50, 50, 0, 0, 0, 0, false},
		{"negative screen", 50, 50, -1, -1, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotX, gotY, gotOK := clampCoords(tt.x, tt.y, tt.w, tt.h)
			if gotOK != tt.wantOK {
				t.Errorf("clampCoords(%d,%d,%d,%d) ok = %v, want %v",
					tt.x, tt.y, tt.w, tt.h, gotOK, tt.wantOK)
			}
			if gotOK && (gotX != tt.wantX || gotY != tt.wantY) {
				t.Errorf("clampCoords(%d,%d,%d,%d) = (%d,%d), want (%d,%d)",
					tt.x, tt.y, tt.w, tt.h, gotX, gotY, tt.wantX, tt.wantY)
			}
		})
	}
}

func TestMapButton(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"left", "left"},
		{"right", "right"},
		{"middle", "center"},
		{"", "left"},
		{"unknown", "left"},
	}
	for _, tt := range tests {
		if got := mapButton(tt.in); got != tt.want {
			t.Errorf("mapButton(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSanitizeModifiers(t *testing.T) {
	tests := []struct {
		name string
		mods []string
		want int // expected count
	}{
		{"all valid", []string{"ctrl", "shift", "alt", "cmd"}, 4},
		{"mixed case", []string{"Ctrl", "SHIFT"}, 2},
		{"invalid filtered", []string{"ctrl", "invalid", "shift"}, 2},
		{"empty", nil, 0},
		{"synonyms", []string{"control", "option", "command", "super"}, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeModifiers(tt.mods)
			if len(got) != tt.want {
				t.Errorf("sanitizeModifiers(%v) = %v (len %d), want len %d", tt.mods, got, len(got), tt.want)
			}
		})
	}
}

func TestValidateKeyEvent(t *testing.T) {
	// Valid key
	if err := ValidateKeyEvent("a", nil); err != nil {
		t.Errorf("valid key: %v", err)
	}
	// Valid key with modifiers
	if err := ValidateKeyEvent("c", []string{"ctrl", "shift"}); err != nil {
		t.Errorf("valid key+mods: %v", err)
	}
	// Invalid key
	var keyErr *InvalidKeyError
	if err := ValidateKeyEvent("nonexistent", nil); !errors.As(err, &keyErr) {
		t.Errorf("invalid key: got %v, want InvalidKeyError", err)
	}
	// Invalid modifier
	var modErr *InvalidModifierError
	if err := ValidateKeyEvent("a", []string{"badmod"}); !errors.As(err, &modErr) {
		t.Errorf("invalid mod: got %v, want InvalidModifierError", err)
	}
	// Case insensitive
	if err := ValidateKeyEvent("A", []string{"CTRL"}); err != nil {
		t.Errorf("case insensitive: %v", err)
	}
}

func TestValidateText(t *testing.T) {
	// Valid text
	if err := ValidateText("hello world"); err != nil {
		t.Errorf("valid text: %v", err)
	}
	// Empty text is valid
	if err := ValidateText(""); err != nil {
		t.Errorf("empty text: %v", err)
	}
	// Too long
	var longErr *TextTooLongError
	if err := ValidateText(string(make([]byte, 257))); !errors.As(err, &longErr) {
		t.Errorf("too long: got %v, want TextTooLongError", err)
	}
	// Control character
	var ctrlErr *ControlCharError
	if err := ValidateText("hello\x00world"); !errors.As(err, &ctrlErr) {
		t.Errorf("control char: got %v, want ControlCharError", err)
	}
	// DEL character
	if err := ValidateText("hello\x7f"); !errors.As(err, &ctrlErr) {
		t.Errorf("DEL char: got %v, want ControlCharError", err)
	}
	// Tab is control char
	if err := ValidateText("\t"); !errors.As(err, &ctrlErr) {
		t.Errorf("tab: got %v, want ControlCharError", err)
	}
}

func TestValidateKeyToggleState(t *testing.T) {
	if err := ValidateKeyToggleState("down"); err != nil {
		t.Errorf("down: %v", err)
	}
	if err := ValidateKeyToggleState("up"); err != nil {
		t.Errorf("up: %v", err)
	}
	var stateErr *InvalidStateError
	if err := ValidateKeyToggleState("invalid"); !errors.As(err, &stateErr) {
		t.Errorf("invalid state: got %v, want InvalidStateError", err)
	}
	if err := ValidateKeyToggleState(""); !errors.As(err, &stateErr) {
		t.Errorf("empty state: got %v, want InvalidStateError", err)
	}
}

func TestValidateScrollAmount(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, 3},    // zero → default
		{-1, 3},   // negative → default
		{5, 5},    // valid
		{20, 20},  // max
		{21, 20},  // over max → capped
		{100, 20}, // way over → capped
	}
	for _, tt := range tests {
		if got := ValidateScrollAmount(tt.in); got != tt.want {
			t.Errorf("ValidateScrollAmount(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestValidateScrollDirection(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"up", "up"},
		{"down", "down"},
		{"left", "left"},
		{"right", "right"},
		{"", "down"},       // default
		{"bad", "down"},    // default
	}
	for _, tt := range tests {
		if got := ValidateScrollDirection(tt.in); got != tt.want {
			t.Errorf("ValidateScrollDirection(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestValidKeysWhitelist(t *testing.T) {
	// Spot check that common keys are in the whitelist
	for _, key := range []string{"a", "z", "0", "9", "f1", "f12", "enter", "tab", "escape", "space"} {
		if !validKeys[key] {
			t.Errorf("validKeys missing expected key: %q", key)
		}
	}
}

func TestValidateInputEventType(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"mouse_move", true},
		{"mouse_click", true},
		{"mouse_scroll", true},
		{"mouse_drag", true},
		{"key_tap", true},
		{"key_toggle", true},
		{"type_text", true},
		{"refresh_screenshot", true},
		{"", false},
		{"unknown", false},
		{"MOUSE_MOVE", false}, // case-sensitive
	}
	for _, tt := range tests {
		if got := ValidateInputEventType(tt.in); got != tt.want {
			t.Errorf("ValidateInputEventType(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
