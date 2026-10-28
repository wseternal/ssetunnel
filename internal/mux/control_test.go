package mux

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestRoundTrip_ResizeMessage verifies that a resize message survives
// a write→read round trip with correct type, stream ID, and payload.
func TestRoundTrip_ResizeMessage(t *testing.T) {
	msg := NewResizeMessage(42, 120, 40)
	var buf bytes.Buffer
	if err := WriteControlMessage(&buf, msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadControlMessage(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Type != MsgResize {
		t.Errorf("type = %q, want %q", got.Type, MsgResize)
	}
	if got.StreamID != 42 {
		t.Errorf("stream_id = %d, want 42", got.StreamID)
	}
	var payload ResizePayload
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Cols != 120 || payload.Rows != 40 {
		t.Errorf("payload = %+v, want {Cols:120 Rows:40}", payload)
	}
}

// TestRoundTrip_TraceMessage verifies a trace message round trip.
func TestRoundTrip_TraceMessage(t *testing.T) {
	event := TraceEvent{
		TS:         "2026-01-01T00:00:00Z",
		EventType:  "pty_read",
		DurationMs: 5,
		Bytes:      1024,
		Message:    "hello",
	}
	msg := NewTraceMessage(7, event)
	var buf bytes.Buffer
	if err := WriteControlMessage(&buf, msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadControlMessage(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Type != MsgTrace {
		t.Errorf("type = %q, want %q", got.Type, MsgTrace)
	}
	if got.StreamID != 7 {
		t.Errorf("stream_id = %d, want 7", got.StreamID)
	}
	var payload TraceEvent
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.EventType != "pty_read" || payload.Bytes != 1024 {
		t.Errorf("payload = %+v, want event_type=pty_read bytes=1024", payload)
	}
}

// TestReadControlMessage_ZeroLength verifies that a zero-length message
// is rejected with an error.
func TestReadControlMessage_ZeroLength(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0, 0, 0, 0}) // length = 0
	_, err := ReadControlMessage(&buf)
	if err == nil {
		t.Fatal("expected error for zero-length message")
	}
	if !strings.Contains(err.Error(), "zero-length") {
		t.Errorf("error = %q, want to contain 'zero-length'", err)
	}
}

// TestReadControlMessage_Oversized verifies that a message exceeding
// maxControlMessageSize is rejected.
func TestReadControlMessage_Oversized(t *testing.T) {
	var buf bytes.Buffer
	// Write length = maxControlMessageSize + 1
	oversized := maxControlMessageSize + 1
	var lenBuf [4]byte
	lenBuf[0] = byte(oversized >> 24)
	lenBuf[1] = byte(oversized >> 16)
	lenBuf[2] = byte(oversized >> 8)
	lenBuf[3] = byte(oversized)
	buf.Write(lenBuf[:])
	_, err := ReadControlMessage(&buf)
	if err == nil {
		t.Fatal("expected error for oversized message")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q, want to contain 'too large'", err)
	}
}

// TestReadControlMessage_MalformedJSON verifies that invalid JSON in the
// payload returns a wrapped error.
func TestReadControlMessage_MalformedJSON(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte(`{not valid json`)
	var lenBuf [4]byte
	lenBuf[0] = byte(len(payload) >> 24)
	lenBuf[1] = byte(len(payload) >> 16)
	lenBuf[2] = byte(len(payload) >> 8)
	lenBuf[3] = byte(len(payload))
	buf.Write(lenBuf[:])
	buf.Write(payload)
	_, err := ReadControlMessage(&buf)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "bad JSON") {
		t.Errorf("error = %q, want to contain 'bad JSON'", err)
	}
}

// TestReadControlMessage_TruncatedLength verifies that reading fewer
// than 4 bytes for the length prefix returns an error.
func TestReadControlMessage_TruncatedLength(t *testing.T) {
	buf := bytes.NewBuffer([]byte{0, 0}) // only 2 bytes
	_, err := ReadControlMessage(buf)
	if err == nil {
		t.Fatal("expected error for truncated length")
	}
	if err != io.ErrUnexpectedEOF && err != io.EOF {
		t.Errorf("error = %v, want io.ErrUnexpectedEOF or io.EOF", err)
	}
}

// TestReadControlMessage_TruncatedPayload verifies that a length prefix
// claiming more bytes than available returns an error.
func TestReadControlMessage_TruncatedPayload(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0, 0, 0, 100}) // claims 100 bytes
	buf.Write([]byte("short"))       // only 5 bytes available
	_, err := ReadControlMessage(&buf)
	if err == nil {
		t.Fatal("expected error for truncated payload")
	}
}

// TestWriteControlMessage_Oversized verifies that writing a message
// exceeding the size limit returns an error.
func TestWriteControlMessage_Oversized(t *testing.T) {
	// Build a valid JSON payload that exceeds maxControlMessageSize
	// when combined with the outer message envelope.
	big := strings.Repeat("x", maxControlMessageSize)
	msg := &ControlMessage{
		Type:    "test",
		Payload: json.RawMessage(`"` + big + `"`),
	}
	var buf bytes.Buffer
	err := WriteControlMessage(&buf, msg)
	if err == nil {
		t.Fatal("expected error for oversized message")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q, want to contain 'too large'", err)
	}
}

// TestMultipleMessages verifies that multiple messages can be written
// and read sequentially without framing errors.
func TestMultipleMessages(t *testing.T) {
	var buf bytes.Buffer
	messages := []*ControlMessage{
		NewResizeMessage(1, 80, 24),
		NewTraceMessage(2, TraceEvent{EventType: "pty_write"}),
		NewResizeMessage(3, 120, 40),
	}
	for _, msg := range messages {
		if err := WriteControlMessage(&buf, msg); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	for i, want := range messages {
		got, err := ReadControlMessage(&buf)
		if err != nil {
			t.Fatalf("read message %d: %v", i, err)
		}
		if got.Type != want.Type {
			t.Errorf("message %d: type = %q, want %q", i, got.Type, want.Type)
		}
		if got.StreamID != want.StreamID {
			t.Errorf("message %d: stream_id = %d, want %d", i, got.StreamID, want.StreamID)
		}
	}
}
