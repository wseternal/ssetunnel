// Package remoteapp implements remote desktop control: screen capture
// via robotgo and input replay (mouse + keyboard) over a yamux stream.
//
// The wire protocol uses typed length-prefixed frames to safely carry
// binary JPEG screenshots and JSON input events over the same stream.
package remoteapp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Frame type identifiers for the yamux stream wire protocol.
const (
	FrameScreenshot byte = 0x01 // Agent → Server: JPEG image data
	FrameInput      byte = 0x02 // Server → Agent: JSON input event
	FrameScreenInfo byte = 0x03 // Agent → Server: JSON screen dimensions
)

// maxFrameSize caps a single frame at 4 MiB. A 1920×1080 JPEG at quality 50
// is typically 50–150 KB; 4 MiB provides ample headroom while preventing
// runaway memory allocation from a misbehaving peer.
const maxFrameSize = 4 << 20

// MaxFrameSize returns the maximum allowed frame size.
func MaxFrameSize() uint32 { return maxFrameSize }

// ErrNotSupported is returned by stub implementations when the binary
// was built without the "remoteapp" build tag.
var ErrNotSupported = errors.New("remote app not supported: build with -tags remoteapp")

// ErrFrameTooLarge is returned when a frame exceeds maxFrameSize.
var ErrFrameTooLarge = errors.New("frame too large")

// WriteFrame writes a typed length-prefixed frame: [type][4-byte BE length][data].
func WriteFrame(w io.Writer, frameType byte, data []byte) error {
	if len(data) > maxFrameSize {
		return fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(data))
	}
	header := [5]byte{frameType}
	binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrame reads a typed length-prefixed frame from r.
// Returns the frame type, payload data, and any error.
func ReadFrame(r io.Reader) (frameType byte, data []byte, err error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	frameType = header[0]
	length := binary.BigEndian.Uint32(header[1:])
	if length > maxFrameSize {
		return 0, nil, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, length)
	}
	if length == 0 {
		return frameType, nil, nil
	}
	data = make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return 0, nil, err
	}
	return frameType, data, nil
}

// InputEvent represents a mouse or keyboard event sent from the browser
// to the agent for replay via robotgo.
type InputEvent struct {
	Type      string   `json:"type"`                // mouse_move, mouse_click, mouse_scroll, mouse_drag, key_tap, key_toggle, type_text
	X         int      `json:"x,omitempty"`          // mouse X coordinate (agent screen pixels)
	Y         int      `json:"y,omitempty"`          // mouse Y coordinate (agent screen pixels)
	Button    string   `json:"button,omitempty"`     // left, right, middle, wheelUp, wheelDown
	Direction string   `json:"direction,omitempty"` // up, down, left, right (scroll)
	Amount    int      `json:"amount,omitempty"`    // scroll amount
	Key       string   `json:"key,omitempty"`       // key name (robotgo key names)
	Modifiers []string `json:"modifiers,omitempty"` // modifier keys: ctrl, shift, alt, cmd/super
	Text      string   `json:"text,omitempty"`      // text for type_text
	State     string   `json:"state,omitempty"`     // down, up (for key_toggle, mouse_drag)
}

// ScreenInfo carries the agent's screen dimensions, sent as the first
// frame on the yamux stream so the server/frontend can scale coordinates.
type ScreenInfo struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}
