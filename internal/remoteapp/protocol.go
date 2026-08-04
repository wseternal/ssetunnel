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
	"sync"
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

// ErrNotSupported is returned by stub implementations when the current
// OS is not supported by the robotgo library (only darwin, windows, linux).
var ErrNotSupported = errors.New("remote app not supported on this OS")

// ErrFrameTooLarge is returned when a frame exceeds maxFrameSize.
var ErrFrameTooLarge = errors.New("frame too large")

// headerPool reuses 5-byte frame headers to avoid per-frame allocations.
var headerPool = sync.Pool{
	New: func() any { b := make([]byte, 5); return &b },
}

// WriteFrame writes a typed length-prefixed frame: [type][4-byte BE length][data].
// The header and payload are written in two calls to avoid allocating a
// combined buffer (~150 KB per screenshot frame). Yamux streams are not
// shared between writers on the same stream, so two writes are safe.
func WriteFrame(w io.Writer, frameType byte, data []byte) error {
	if len(data) > maxFrameSize {
		return fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(data))
	}
	hp := headerPool.Get().(*[]byte)
	header := *hp
	header[0] = frameType
	binary.BigEndian.PutUint32(header[1:5], uint32(len(data)))
	_, err := w.Write(header)
	if err != nil {
		headerPool.Put(hp)
		return err
	}
	headerPool.Put(hp)
	if len(data) > 0 {
		_, err = w.Write(data)
	}
	return err
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

// ReadFrameInto reads a typed length-prefixed frame into a caller-supplied
// buffer, avoiding per-frame allocations. Returns the frame type, number of
// payload bytes read, and any error. The buffer must have capacity >= the
// frame's payload length; otherwise ErrFrameTooLarge is returned.
func ReadFrameInto(r io.Reader, buf []byte) (frameType byte, n int, err error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, 0, err
	}
	frameType = header[0]
	length := binary.BigEndian.Uint32(header[1:])
	if length > maxFrameSize {
		return 0, 0, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, length)
	}
	if length == 0 {
		return frameType, 0, nil
	}
	if int(length) > cap(buf) {
		return 0, 0, fmt.Errorf("%w: buffer too small (%d < %d)", ErrFrameTooLarge, cap(buf), length)
	}
	if _, err := io.ReadFull(r, buf[:length]); err != nil {
		return 0, 0, err
	}
	return frameType, int(length), nil
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
