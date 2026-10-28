package mux

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Control stream marker: the first line written by the server when opening
// the control stream. The agent's accept loop reads this line to distinguish
// the control stream from data streams.
const ControlStreamMarker = "__control__\n"

// Message type constants for the control stream protocol.
const (
	// MsgResize carries a PTY resize command from server to agent.
	MsgResize = "resize"

	// MsgTrace carries a diagnostic trace event (bidirectional: agent →
	// server for shell tracing, server → agent for server-side events).
	MsgTrace = "trace"
)

// maxControlMessageSize caps a single control message at 64 KiB.
// Real messages are tiny (~100 bytes for resize, ~200 bytes for trace).
// This prevents runaway memory from a misbehaving peer.
const maxControlMessageSize = 64 << 10

// ControlMessage is one framed message on the control stream.
//
// Wire format: [4-byte big-endian length][JSON payload]
//
// JSON payload:
//
//	{
//	  "type":      "resize" | "trace" | ...,
//	  "stream_id": <yamux stream ID of the target data stream>,
//	  "payload":   { type-specific fields }
//	}
//
// stream_id correlates the message to a specific data stream. For
// agent→server trace messages, it identifies which data session the
// trace originated from. For server→agent resize, it targets the
// data stream whose PTY should be resized.
type ControlMessage struct {
	Type     string          `json:"type"`
	StreamID uint32          `json:"stream_id"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

// ResizePayload is the JSON payload for MsgResize messages.
type ResizePayload struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// TraceEvent is the JSON payload for MsgTrace messages.
type TraceEvent struct {
	TS         string `json:"ts"`         // ISO-8601 timestamp
	EventType  string `json:"event_type"` // pty_read, pty_write, stream_read, stream_write, resize
	DurationMs int64  `json:"duration_ms,omitempty"`
	Bytes      int64  `json:"bytes,omitempty"`
	Message    string `json:"message,omitempty"`
}

// ReadControlMessage reads one length-prefixed JSON control message
// from r. Returns an error if the message exceeds maxControlMessageSize
// or the JSON is malformed.
func ReadControlMessage(r io.Reader) (*ControlMessage, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 {
		return nil, fmt.Errorf("control: zero-length message")
	}
	if n > maxControlMessageSize {
		return nil, fmt.Errorf("control: message too large: %d bytes (max %d)", n, maxControlMessageSize)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	var msg ControlMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("control: bad JSON: %w", err)
	}
	return &msg, nil
}

// WriteControlMessage serializes msg as JSON and writes it with a
// 4-byte big-endian length prefix to w. The write is NOT atomic —
// callers must ensure exclusive access to w.
func WriteControlMessage(w io.Writer, msg *ControlMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("control: marshal: %w", err)
	}
	if len(data) > maxControlMessageSize {
		return fmt.Errorf("control: message too large: %d bytes (max %d)", len(data), maxControlMessageSize)
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// NewResizeMessage creates a resize control message targeting the given
// data stream.
func NewResizeMessage(streamID uint32, cols, rows uint16) *ControlMessage {
	payload, _ := json.Marshal(ResizePayload{Cols: cols, Rows: rows})
	return &ControlMessage{
		Type:     MsgResize,
		StreamID: streamID,
		Payload:  payload,
	}
}

// NewTraceMessage creates a trace control message for the given data stream.
func NewTraceMessage(streamID uint32, event TraceEvent) *ControlMessage {
	payload, _ := json.Marshal(event)
	return &ControlMessage{
		Type:     MsgTrace,
		StreamID: streamID,
		Payload:  payload,
	}
}
