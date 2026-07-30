package transport

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// countingFlusher wraps an http.Flusher to count Flush calls, proving
// the codec flushes once per frame so proxies forward data immediately.
type countingFlusher struct {
	flushes int
}

func (c *countingFlusher) Flush() { c.flushes++ }

func TestSSERoundTrip(t *testing.T) {
	t.Parallel()
	payloads := [][]byte{
		{},
		{0x00},
		[]byte("hello, world"),
		[]byte("line one\nline two\n\nwith blank line"),
		bytes.Repeat([]byte{0x00, 0xff, 0x42, 0x0a, 0x0d}, 1000),
	}
	for i, want := range payloads {
		t.Run(fmt.Sprintf("payload_%d_len_%d", i, len(want)), func(t *testing.T) {
			rec := httptest.NewRecorder()
			if err := WriteFrame(rec, rec, want); err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}
			dec := newSSEDecoder()
			events, err := dec.Feed(rec.Body.Bytes())
			if err != nil {
				t.Fatalf("Feed: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("got %d events, want 1", len(events))
			}
			if !bytes.Equal(events[0].Data, want) {
				t.Fatalf("payload mismatch: got %d bytes, want %d", len(events[0].Data), len(want))
			}
			if rest := dec.Rest(); len(rest) != 0 {
				t.Fatalf("decoder left %d buffered bytes, want 0", len(rest))
			}
		})
	}
}

func TestSSEHeadersAndFlushPerFrame(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	f := &countingFlusher{}
	WriteHeaders(rec)
	const frames = 5
	for i := 0; i < frames; i++ {
		if err := WriteFrame(rec, f, []byte{byte(i)}); err != nil {
			t.Fatalf("WriteFrame %d: %v", i, err)
		}
	}
	if f.flushes != frames {
		t.Fatalf("got %d flushes for %d frames, want one flush per frame", f.flushes, frames)
	}
	h := rec.Header()
	if got := h.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if got := h.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	if got := h.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", got)
	}
}

func TestSSEHeartbeatFiltered(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	f := &countingFlusher{}
	// Heartbeats keep middleboxes alive; they must never surface as data.
	for i := 0; i < 3; i++ {
		if err := WriteHeartbeat(rec, f); err != nil {
			t.Fatalf("WriteHeartbeat: %v", err)
		}
	}
	if err := WriteFrame(rec, f, []byte("real data")); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	if err := WriteHeartbeat(rec, f); err != nil {
		t.Fatalf("WriteHeartbeat: %v", err)
	}
	if f.flushes != 5 {
		t.Fatalf("got %d flushes, want 5 (heartbeats flush too)", f.flushes)
	}
	dec := newSSEDecoder()
	events, err := dec.Feed(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(events) != 1 || string(events[0].Data) != "real data" {
		t.Fatalf("got %d events %q, want exactly [\"real data\"]", len(events), events)
	}
}

func TestSSESplitLineReassembly(t *testing.T) {
	t.Parallel()
	// Two frames plus a heartbeat, fed to the decoder in every possible
	// chunking: split points inside the base64 must not corrupt output.
	var wire bytes.Buffer
	wire.WriteString(": ka\n\n")
	for _, p := range []string{"first", "second"} {
		fmt.Fprintf(&wire, "data: %s\n\n", base64.StdEncoding.EncodeToString([]byte(p)))
	}
	raw := wire.Bytes()
	for split := 0; split <= len(raw); split++ {
		dec := newSSEDecoder()
		var events []SSEEvent
		for _, chunk := range [][]byte{raw[:split], raw[split:]} {
			got, err := dec.Feed(chunk)
			if err != nil {
				t.Fatalf("split %d: Feed: %v", split, err)
			}
			events = append(events, got...)
		}
		if len(events) != 2 || string(events[0].Data) != "first" || string(events[1].Data) != "second" {
			t.Fatalf("split %d: got %q, want [first second]", split, events)
		}
	}
}

func TestSSEDecoderMultiFrameSingleFeed(t *testing.T) {
	t.Parallel()
	dec := newSSEDecoder()
	var wire bytes.Buffer
	const n = 100
	for i := 0; i < n; i++ {
		fmt.Fprintf(&wire, "data: %s\n\n", base64.StdEncoding.EncodeToString([]byte{byte(i)}))
	}
	events, err := dec.Feed(wire.Bytes())
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(events) != n {
		t.Fatalf("got %d events, want %d", len(events), n)
	}
	for i, ev := range events {
		if len(ev.Data) != 1 || ev.Data[0] != byte(i) {
			t.Fatalf("event %d = %v, want [%d]", i, ev, i)
		}
	}
}

func TestSSEDecoderOversizedLine(t *testing.T) {
	t.Parallel()
	dec := newSSEDecoder()
	// A single line longer than the 1 MiB guard must error, not buffer
	// unboundedly.
	_, err := dec.Feed([]byte("data: " + strings.Repeat("A", maxSSELineSize+1)))
	if err == nil {
		t.Fatal("expected oversized-line error, got nil")
	}
	if !strings.Contains(err.Error(), "line too long") {
		t.Fatalf("error = %v, want it to mention line too long", err)
	}
	// The decoder stays usable for the caller to abandon; a second feed
	// keeps reporting the same condition rather than panicking.
	if _, err2 := dec.Feed([]byte("more")); err2 == nil {
		t.Fatal("expected sticky oversized-line error, got nil")
	}
}

func TestSSEDecoderInvalidBase64(t *testing.T) {
	t.Parallel()
	dec := newSSEDecoder()
	_, err := dec.Feed([]byte("data: !!!not-base64!!!\n\n"))
	if err == nil {
		t.Fatal("expected base64 decode error, got nil")
	}
}
