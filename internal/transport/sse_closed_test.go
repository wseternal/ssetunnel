package transport

import (
	"bytes"
	"net/http/httptest"
	"testing"
)

// TestWriteClosedFrame verifies the wire format of the terminal
// "event: closed" frame matches what the frontend SSE parser expects:
// a named event with an empty data payload, terminated by "\n\n".
//
// The frontend's parseSSEEvents filters events whose data is empty, so
// the closed signal relies on the `type` field being populated. This
// test locks in the contract: type == "closed", data == "".
func TestWriteClosedFrame(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	if err := WriteClosedFrame(rec, rec); err != nil {
		t.Fatalf("WriteClosedFrame: %v", err)
	}
	want := "event: closed\ndata:\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("wire format:\n got  %q\n want %q", got, want)
	}
	// The decoder surfaces the closed event with an empty payload.
	dec := newSSEDecoder()
	events, err := dec.Feed(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != SSEEventTypeClosed {
		t.Fatalf("event type = %q, want %q", events[0].Type, SSEEventTypeClosed)
	}
	if len(events[0].Data) != 0 {
		t.Fatalf("event data = %q, want empty", events[0].Data)
	}
}

// TestWriteEventFrame verifies the generic typed-event writer produces
// the right wire format for an arbitrary event type + base64 payload.
func TestWriteEventFrame(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	payload := []byte(`{"batch":65536,"compress":true}`)
	if err := WriteEventFrame(rec, rec, "tune", payload); err != nil {
		t.Fatalf("WriteEventFrame: %v", err)
	}
	dec := newSSEDecoder()
	events, err := dec.Feed(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != "tune" {
		t.Fatalf("event type = %q, want %q", events[0].Type, "tune")
	}
	if !bytes.Equal(events[0].Data, payload) {
		t.Fatalf("event data = %q, want %q", events[0].Data, payload)
	}
}

// TestEncodeDecodeResize_RoundTrip locks in the length-prefixed wire
// format for PTY resize messages: 0x00 | uint16be(len) | JSON payload.
// The format is shared between the server's resizeForwarder / connect
// bridge and the agent's forwardStreamToPTY; any drift between the
// encoder and decoder would silently desync the PTY size.
func TestEncodeDecodeResize_RoundTrip(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"cols":120,"rows":40}`)
	frame := EncodeResize(payload)
	if frame == nil {
		t.Fatal("EncodeResize returned nil for valid payload")
	}
	if frame[0] != 0 {
		t.Fatalf("leading marker = 0x%02x, want 0x00", frame[0])
	}
	wantLen := len(payload)
	gotLen := int(frame[1])<<8 | int(frame[2])
	if gotLen != wantLen {
		t.Fatalf("declared length = %d, want %d", gotLen, wantLen)
	}
	decoded, err := DecodeResize(frame[1], frame[2], frame[3:], 1024)
	if err != nil {
		t.Fatalf("DecodeResize: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("round-trip payload = %q, want %q", decoded, payload)
	}
}

// TestEncodeResize_Bounds verifies the encoder rejects empty and
// oversize payloads so callers cannot emit malformed frames.
func TestEncodeResize_Bounds(t *testing.T) {
	t.Parallel()
	if got := EncodeResize(nil); got != nil {
		t.Fatalf("EncodeResize(nil) = %v, want nil", got)
	}
	if got := EncodeResize([]byte{}); got != nil {
		t.Fatalf("EncodeResize(empty) = %v, want nil", got)
	}
	oversize := make([]byte, 0x10000)
	if got := EncodeResize(oversize); got != nil {
		t.Fatalf("EncodeResize(65536 B) = %d bytes, want nil", len(got))
	}
	// At the 0xFFFF boundary the encoder must still succeed.
	edge := make([]byte, 0xFFFF)
	got := EncodeResize(edge)
	if got == nil {
		t.Fatal("EncodeResize(65535 B) = nil, want non-nil")
	}
	if len(got) != 3+0xFFFF {
		t.Fatalf("encoded length = %d, want %d", len(got), 3+0xFFFF)
	}
}

// TestDecodeResize_Errors verifies the decoder rejects truncated and
// out-of-bounds frames rather than returning partial payloads.
func TestDecodeResize_Errors(t *testing.T) {
	t.Parallel()
	if _, err := DecodeResize(0, 0, nil, 1024); err == nil {
		t.Fatal("DecodeResize(len=0) succeeded, want error")
	}
	if _, err := DecodeResize(0x04, 0x01, []byte("x"), 1024); err == nil {
		t.Fatal("DecodeResize(truncated) succeeded, want error")
	}
	if _, err := DecodeResize(0x10, 0x00, make([]byte, 4096), 1024); err == nil {
		t.Fatal("DecodeResize(oversize) succeeded, want error")
	}
}
