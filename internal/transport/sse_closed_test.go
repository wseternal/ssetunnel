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
