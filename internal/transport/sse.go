package transport

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
)

// maxSSELineSize bounds one encoded SSE line so a corrupt or hostile
// peer cannot make the decoder buffer unboundedly.
const maxSSELineSize = 1 << 20 // 1 MiB

// WriteHeaders sets the SSE response headers. X-Accel-Buffering: no
// stops nginx-style proxies from buffering the stream.
func WriteHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
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

// WriteHeartbeat writes an SSE comment keepalive and flushes. Comments
// are ignored by decoders, so heartbeats never surface as data.
func WriteHeartbeat(w io.Writer, f http.Flusher) error {
	if _, err := io.WriteString(w, ": ka\n\n"); err != nil {
		return fmt.Errorf("write sse heartbeat: %w", err)
	}
	f.Flush()
	return nil
}

// sseDecoder incrementally decodes an SSE stream: Feed accepts arbitrary
// byte chunks and returns the payloads of complete events.
type sseDecoder struct {
	buf     []byte // partial line bytes not yet terminated by \n
	data    []byte // decoded payload of the event currently in progress
	hasData bool   // whether the current event carried any data line
	err     error  // sticky protocol error
}

func newSSEDecoder() *sseDecoder { return &sseDecoder{} }

// Feed appends p to the stream and returns every event payload completed
// by it. Comment lines (heartbeats) and unknown fields are dropped.
func (d *sseDecoder) Feed(p []byte) ([][]byte, error) {
	if d.err != nil {
		return nil, d.err
	}
	d.buf = append(d.buf, p...)
	var events [][]byte
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
				events = append(events, d.data)
				d.data, d.hasData = nil, false
			}
		case line[0] == ':':
			// Comment (heartbeat): ignore.
		case bytes.HasPrefix(line, []byte("data: ")):
			if err := d.appendData(line[len("data: "):]); err != nil {
				return nil, err
			}
		case bytes.Equal(line, []byte("data:")):
			d.hasData = true // empty payload frame
		default:
			// Unknown SSE field (event:, id:, retry:): ignore.
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
