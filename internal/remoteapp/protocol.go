// Package remoteapp implements remote desktop control: screen capture
// via robotgo and input replay (mouse + keyboard) over a yamux stream.
//
// The wire protocol uses typed length-prefixed frames to safely carry
// binary JPEG screenshots and JSON input events over the same stream.
package remoteapp

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Frame type identifiers for the yamux stream wire protocol.
const (
	FrameScreenshot    byte = 0x01 // Agent → Server: [8-byte BE UnixMilli timestamp][JPEG data]
	FrameInput         byte = 0x02 // Server → Agent: JSON input event
	FrameScreenInfo    byte = 0x03 // Agent → Server: JSON screen dimensions
	FrameLogEvent      byte = 0x04 // Agent → Server: JSON log event for console observability
	FrameScreenshotAck byte = 0x05 // Server → Agent: 8-byte BE UnixMilli (ACK for received screenshot)
	FrameInputAck      byte = 0x06 // Agent → Server: JSON ack for received input event
)

// ScreenshotTimestampSize is the byte length of the Unix-millisecond timestamp
// prepended to JPEG data inside a FrameScreenshot payload.
const ScreenshotTimestampSize = 8

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
// combined buffer (~150 KB per screenshot frame).
//
// Callers MUST ensure exclusive access to w for the duration of this call;
// the two writes are NOT atomic. Use a lockedWriter for concurrent access.
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
	X         int      `json:"x,omitempty"`         // mouse X coordinate (agent screen pixels)
	Y         int      `json:"y,omitempty"`         // mouse Y coordinate (agent screen pixels)
	Button    string   `json:"button,omitempty"`    // left, right, middle, wheelUp, wheelDown
	Direction string   `json:"direction,omitempty"` // up, down, left, right (scroll)
	Amount    int      `json:"amount,omitempty"`    // scroll amount
	Key       string   `json:"key,omitempty"`       // key name (robotgo key names)
	Modifiers []string `json:"modifiers,omitempty"` // modifier keys: ctrl, shift, alt, cmd/super
	Text      string   `json:"text,omitempty"`      // text for type_text
	State     string   `json:"state,omitempty"`     // down, up (for key_toggle, mouse_drag)
}

// InputAck is sent from the agent to the server to acknowledge receipt
// of an input event. The server forwards it as an SSE "inputack" event
// so the console UI can display live feedback on the screenshot.
type InputAck struct {
	Type   string `json:"type"`             // echoed input event type (mouse_click, key_tap, ...)
	Detail string `json:"detail,omitempty"` // brief human-readable detail (button, key name, ...)
}

// WriteInputAck serializes an InputAck and writes it as a FrameInputAck frame.
func WriteInputAck(w io.Writer, ack InputAck) error {
	data, err := json.Marshal(ack)
	if err != nil {
		return err
	}
	return WriteFrame(w, FrameInputAck, data)
}

// ParseInputAck deserializes an InputAck from a FrameInputAck payload.
// Returns ok=false if the payload is not valid JSON.
func ParseInputAck(data []byte) (InputAck, bool) {
	var ack InputAck
	if err := json.Unmarshal(data, &ack); err != nil {
		return InputAck{}, false
	}
	return ack, true
}

// ScreenInfo carries the agent's screen dimensions, sent as the first
// frame on the yamux stream so the server/frontend can scale coordinates.
type ScreenInfo struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// LogEvent carries a structured observability event from the agent to the
// server, which forwards it to the console frontend as an SSE `event: log`.
type LogEvent struct {
	TS       string `json:"ts"`  // ISO-8601 timestamp
	Severity string `json:"sev"` // info, warn, error
	Source   string `json:"src"` // agent or server
	Message  string `json:"msg"`
}

// WriteLogEvent serializes a LogEvent and writes it as a FrameLogEvent frame.
// NOT safe for concurrent use on the same writer. For concurrent access,
// wrap the writer in a lockedWriter and use its writeLogEvent method instead.
func WriteLogEvent(w io.Writer, severity, message string) error {
	evt := LogEvent{
		TS:       time.Now().UTC().Format(time.RFC3339Nano),
		Severity: severity,
		Source:   "agent",
		Message:  message,
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	return WriteFrame(w, FrameLogEvent, data)
}

// WriteScreenshotWithTimestamp writes a FrameScreenshot with an 8-byte
// big-endian Unix-millisecond timestamp prepended to the JPEG payload.
// Wire format: [FrameScreenshot header][8-byte BE UnixMilli][JPEG data].
// Writes header, timestamp, and JPEG as three separate writes to avoid
// allocating a combined ~150 KB payload buffer.
//
// Callers MUST ensure exclusive access to w for the duration of this call;
// the three writes are NOT atomic. Use a lockedWriter for concurrent access.
func WriteScreenshotWithTimestamp(w io.Writer, jpegData []byte, ts time.Time) error {
	totalLen := ScreenshotTimestampSize + len(jpegData)
	if totalLen > maxFrameSize {
		return fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, totalLen)
	}
	hp := headerPool.Get().(*[]byte)
	header := *hp
	header[0] = FrameScreenshot
	binary.BigEndian.PutUint32(header[1:5], uint32(totalLen))
	_, err := w.Write(header)
	headerPool.Put(hp)
	if err != nil {
		return err
	}
	var tsBuf [ScreenshotTimestampSize]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(ts.UnixMilli()))
	if _, err := w.Write(tsBuf[:]); err != nil {
		return err
	}
	if len(jpegData) > 0 {
		_, err = w.Write(jpegData)
	}
	return err
}

// ParseScreenshotTimestamp splits a FrameScreenshot payload into the
// 8-byte timestamp and the remaining JPEG data. Returns ok=false if the
// payload is too short to contain a valid timestamp.
func ParseScreenshotTimestamp(data []byte) (ts time.Time, jpeg []byte, ok bool) {
	if len(data) < ScreenshotTimestampSize {
		return time.Time{}, nil, false
	}
	ms := binary.BigEndian.Uint64(data[:ScreenshotTimestampSize])
	return time.UnixMilli(int64(ms)), data[ScreenshotTimestampSize:], true
}

// WriteScreenshotAck writes a FrameScreenshotAck frame carrying the
// 8-byte big-endian Unix-millisecond timestamp to acknowledge receipt.
func WriteScreenshotAck(w io.Writer, ts time.Time) error {
	var buf [ScreenshotTimestampSize]byte
	binary.BigEndian.PutUint64(buf[:], uint64(ts.UnixMilli()))
	return WriteFrame(w, FrameScreenshotAck, buf[:])
}

// ParseScreenshotAck reads the 8-byte Unix-millisecond timestamp from
// a FrameScreenshotAck payload. Returns ok=false if the payload is
// the wrong size.
func ParseScreenshotAck(data []byte) (time.Time, bool) {
	if len(data) != ScreenshotTimestampSize {
		return time.Time{}, false
	}
	ms := binary.BigEndian.Uint64(data)
	return time.UnixMilli(int64(ms)), true
}

// ErrWriterClosed is returned when writing to a closed lockedWriter.
var ErrWriterClosed = errors.New("writer closed")

// lockedWriter wraps an io.Writer with a mutex to serialize frame writes.
// It implements io.Writer so it can be passed to functions expecting io.Writer
// (e.g., CaptureLoop), while also providing atomic frame-level methods.
type lockedWriter struct {
	mu     sync.Mutex
	w      io.Writer
	closed bool
}

// Write implements io.Writer with mutex protection.
// This makes WriteFrame calls from non-aware callers safe.
func (lw *lockedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if lw.closed {
		return 0, ErrWriterClosed
	}
	return lw.w.Write(p)
}

// writeFrame writes a complete typed frame (header + data) under a single
// lock hold, preventing interleaving with concurrent writes.
func (lw *lockedWriter) writeFrame(frameType byte, data []byte) error {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if lw.closed {
		return ErrWriterClosed
	}
	return WriteFrame(lw.w, frameType, data)
}

// writeLogEvent serializes a LogEvent and writes it as a FrameLogEvent frame
// under a single lock hold.
func (lw *lockedWriter) writeLogEvent(severity, message string) error {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if lw.closed {
		return ErrWriterClosed
	}
	return WriteLogEvent(lw.w, severity, message)
}

// writeScreenshotWithTimestamp writes a FrameScreenshot with an 8-byte
// timestamp prefix under a single lock hold. Uses three separate writes
// (header + timestamp + JPEG) to avoid allocating a combined payload buffer.
func (lw *lockedWriter) writeScreenshotWithTimestamp(jpegData []byte, ts time.Time) error {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if lw.closed {
		return ErrWriterClosed
	}
	return WriteScreenshotWithTimestamp(lw.w, jpegData, ts)
}

// writeInputAck writes a FrameInputAck under a single lock hold.
func (lw *lockedWriter) writeInputAck(ack InputAck) error {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if lw.closed {
		return ErrWriterClosed
	}
	return WriteInputAck(lw.w, ack)
}

// close marks the writer as closed, preventing further writes.
func (lw *lockedWriter) close() {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	lw.closed = true
}
