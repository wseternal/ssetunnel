package transport

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
)

// SSEEventTypeTune is the SSE event type for auto-tuner control frames.
const SSEEventTypeTune = "tune"

// SSEEventTypeClosed is the SSE event type emitted when the server
// terminates an SSE stream deliberately (e.g., the backing session
// died). The frontend treats it as a signal to auto-reconnect rather
// than a permanent failure.
const SSEEventTypeClosed = "closed"

// maxSSELineSize bounds one encoded SSE line so a corrupt or hostile
// peer cannot make the decoder buffer unboundedly.
const maxSSELineSize = 1 << 20 // 1 MiB

// WriteHeaders sets the SSE response headers. X-Accel-Buffering: no
// stops nginx-style proxies from buffering the stream. Cache-Control
// forbids storage (not just revalidation) because shell output often
// contains secrets (pasted credentials, env vars, file contents).
func WriteHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	h.Set("Pragma", "no-cache")
	h.Set("Expires", "0")
	h.Set("X-Accel-Buffering", "no")
}

// WriteFrame writes payload as one base64-encoded SSE data frame and
// flushes immediately: middleboxes forward buffered bytes only on flush.
func WriteFrame(w io.Writer, f http.Flusher, payload []byte) error {
	var buf bytes.Buffer
	buf.WriteString("data: ")
	buf.WriteString(base64.StdEncoding.EncodeToString(payload))
	buf.WriteString("\n\n")
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write sse frame: %w", err)
	}
	f.Flush()
	return nil
}

// WriteTuneFrame writes an SSE event: tune control frame with base64-encoded
// JSON payload. The agent's SSE decoder base64-decodes all data: lines,
// so tune frames must be encoded identically to WriteFrame payloads.
func WriteTuneFrame(w io.Writer, f http.Flusher, jsonPayload []byte) error {
	return WriteEventFrame(w, f, SSEEventTypeTune, jsonPayload)
}

// WriteEventFrame writes one typed SSE event with a base64-encoded data
// payload and flushes immediately. Typed events surface in the frontend
// parser as SSEEvent{Type: eventType, Data: <decoded>}.
func WriteEventFrame(w io.Writer, f http.Flusher, eventType string, payload []byte) error {
	var buf bytes.Buffer
	buf.WriteString("event: ")
	buf.WriteString(eventType)
	buf.WriteByte('\n')
	buf.WriteString("data: ")
	buf.WriteString(base64.StdEncoding.EncodeToString(payload))
	buf.WriteString("\n\n")
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write sse %s frame: %w", eventType, err)
	}
	f.Flush()
	return nil
}

// WriteClosedFrame writes the terminal SSE "event: closed" frame that
// tells the frontend the server is ending the stream deliberately
// (backing session died, explicit teardown, etc.). Payload is empty.
func WriteClosedFrame(w io.Writer, f http.Flusher) error {
	if _, err := io.WriteString(w, "event: closed\ndata:\n\n"); err != nil {
		return fmt.Errorf("write sse closed frame: %w", err)
	}
	f.Flush()
	return nil
}

// WriteHeartbeat writes an SSE comment keepalive and flushes. Comments
// are ignored by decoders, so heartbeats never surface as data.
func WriteHeartbeat(w io.Writer, f http.Flusher) error {
	if _, err := io.WriteString(w, ": ka\n\n"); err != nil {
		return fmt.Errorf("write sse heartbeat: %w", err)
	}
	f.Flush()
	return nil
}

// EncodeResize encodes a PTY resize JSON payload into the
// length-prefixed wire format shared by the server's resizeForwarder
// and the connect bridge's resize multiplexer:
//
//	0x00 | uint16be(len) | JSON payload
//
// The 2-byte length bounds the agent-side read so a stray NUL byte
// from user input (Ctrl-Space, Ctrl-@) costs at most a bounded
// hiccup instead of swallowing keystrokes into an unbounded buffer
// until a literal 0x0A. Returns nil when jsonPayload is empty or
// exceeds 0xFFFF bytes (callers are expected to drop oversize
// messages rather than truncate).
func EncodeResize(jsonPayload []byte) []byte {
	n := len(jsonPayload)
	if n == 0 || n > 0xFFFF {
		return nil
	}
	out := make([]byte, 3+n)
	out[0] = 0
	out[1] = byte(n >> 8)
	out[2] = byte(n)
	copy(out[3:], jsonPayload)
	return out
}

// DecodeResize is the inverse of EncodeResize: given a wire frame
// (without the leading 0x00 marker, which the caller has already
// consumed), reads the 2-byte length and payload, returning the
// JSON body. Returns an error when the frame is truncated or the
// declared length exceeds maxResizePayload (1024 B in the agent;
// passed as a parameter so server-side tests can pass a different
// bound).
func DecodeResize(lenByte0, lenByte1 byte, payload []byte, maxLen int) ([]byte, error) {
	n := int(lenByte0)<<8 | int(lenByte1)
	if n == 0 || n > maxLen {
		return nil, fmt.Errorf("resize payload length %d out of bounds (max %d)", n, maxLen)
	}
	if len(payload) < n {
		return nil, fmt.Errorf("resize payload truncated: have %d, want %d", len(payload), n)
	}
	return payload[:n], nil
}

// SSEEvent is one parsed SSE event with its type and decoded data.
// Type is empty for data-only events (no event: field).
type SSEEvent struct {
	Type string // e.g. "tune", or empty for plain data
	Data []byte // decoded payload
}

// sseDecoder incrementally decodes an SSE stream: Feed accepts arbitrary
// byte chunks and returns complete typed events.
type sseDecoder struct {
	buf       []byte // partial line bytes not yet terminated by \n
	data      []byte // decoded payload of the event currently in progress
	eventType string // current event type from event: field
	hasData   bool   // whether the current event carried any data line
	err       error  // sticky protocol error
}

func newSSEDecoder() *sseDecoder { return &sseDecoder{} }

// Feed appends p to the stream and returns every complete event.
// Comment lines (heartbeats) are dropped. Unknown fields are ignored.
func (d *sseDecoder) Feed(p []byte) ([]SSEEvent, error) {
	if d.err != nil {
		return nil, d.err
	}
	d.buf = append(d.buf, p...)
	var events []SSEEvent
	for {
		i := bytes.IndexByte(d.buf, '\n')
		if i < 0 {
			if len(d.buf) > maxSSELineSize {
				d.err = fmt.Errorf("sse line too long: %d bytes > %d", len(d.buf), maxSSELineSize)
				return nil, d.err
			}
			return events, nil
		}
		line := d.buf[:i]
		d.buf = d.buf[i+1:]
		if len(line) > maxSSELineSize {
			d.err = fmt.Errorf("sse line too long: %d bytes > %d", len(line), maxSSELineSize)
			return nil, d.err
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		switch {
		case len(line) == 0:
			// Blank line: end of event. Emit only if it carried data.
			if d.hasData {
				events = append(events, SSEEvent{Type: d.eventType, Data: d.data})
				d.data, d.hasData, d.eventType = nil, false, ""
			}
		case line[0] == ':':
			// Comment (heartbeat): ignore.
		case bytes.HasPrefix(line, []byte("data: ")):
			if err := d.appendData(line[len("data: "):]); err != nil {
				return nil, err
			}
		case bytes.Equal(line, []byte("data:")):
			d.hasData = true // empty payload frame
		case bytes.HasPrefix(line, []byte("event: ")):
			d.eventType = string(line[len("event: "):])
		case bytes.Equal(line, []byte("event:")):
			d.eventType = "" // empty event type
		default:
			// Unknown SSE field (id:, retry:): ignore.
		}
	}
}

// appendData decodes one base64 data line onto the current event.
func (d *sseDecoder) appendData(enc []byte) error {
	payload, err := base64.StdEncoding.DecodeString(string(enc))
	if err != nil {
		d.err = fmt.Errorf("decode sse data: %w", err)
		return d.err
	}
	d.data = append(d.data, payload...)
	d.hasData = true
	return nil
}

// Rest returns the buffered bytes that do not yet form a complete line.
func (d *sseDecoder) Rest() []byte { return d.buf }
