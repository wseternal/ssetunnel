package agent

import (
	"encoding/json"
	"log"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/wseternal/ssetunnel/internal/mux"
)

// RegisteredStream is the interface that data stream handlers implement
// to receive control messages routed by stream ID. The agent's control
// dispatcher maintains a registry of these, keyed by yamux stream ID.
type RegisteredStream interface {
	// HandleResize applies a PTY resize to the data session.
	HandleResize(cols, rows uint16)
	// HandleTrace records a diagnostic trace event.
	HandleTrace(event mux.TraceEvent)
}

// streamRegistry maps yamux stream IDs to registered data stream handlers.
// Thread-safe: the control dispatcher reads, data stream goroutines register
// and unregister.
type streamRegistry struct {
	mu      sync.RWMutex
	streams map[uint32]RegisteredStream
}

func newStreamRegistry() *streamRegistry {
	return &streamRegistry{streams: make(map[uint32]RegisteredStream)}
}

func (r *streamRegistry) Register(id uint32, s RegisteredStream) {
	r.mu.Lock()
	r.streams[id] = s
	r.mu.Unlock()
}

func (r *streamRegistry) Unregister(id uint32) {
	r.mu.Lock()
	delete(r.streams, id)
	r.mu.Unlock()
}

func (r *streamRegistry) Lookup(id uint32) (RegisteredStream, bool) {
	r.mu.RLock()
	s, ok := r.streams[id]
	r.mu.RUnlock()
	return s, ok
}

// controlSender is a function that sends a control message. The agent's
// control handler installs this so data stream handlers (e.g., shell)
// can send trace events back to the server.
type controlSender func(msg *mux.ControlMessage)

// openControlStream opens a dedicated control stream from the agent to
// the server. The agent writes the control marker so the server can
// identify the stream. A background goroutine reads commands (resize)
// from the server while the installed sendControl function lets data
// streams push trace events back.
//
// On failure, the yamux session is closed so Run reconnects with a
// fresh session — a degraded session (no control channel) is worse
// than a transient disconnect.
func (a *Agent) openControlStream(sess *yamux.Session) {
	cs, err := sess.OpenStream()
	if err != nil {
		log.Printf("agent: open control stream failed (session will reconnect): %v", err)
		sess.Close()
		return
	}
	if _, err := cs.Write([]byte(mux.ControlStreamMarker)); err != nil {
		log.Printf("agent: write control marker failed (session will reconnect): %v", err)
		cs.Close()
		sess.Close()
		return
	}
	go a.handleControlStream(cs)
}

// handleControlStream reads control messages from the server and dispatches
// them to the appropriate data stream handler via the stream registry.
// It runs until the stream closes or an error occurs.
func (a *Agent) handleControlStream(stream net.Conn) {
	// Install the control sender so data streams can push trace events.
	// A dedicated writeMu serializes concurrent writes (SendTrace) to
	// the control stream, since WriteControlMessage is NOT atomic.
	var writeMu sync.Mutex
	a.controlMu.Lock()
	a.controlGen++
	gen := a.controlGen
	a.sendControl = func(msg *mux.ControlMessage) {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := mux.WriteControlMessage(stream, msg); err != nil {
			log.Printf("agent: control send failed: %v", err)
		}
	}
	a.traceDropLogged.Store(false) // reset drop log for new control stream
	a.controlMu.Unlock()
	defer func() {
		a.controlMu.Lock()
		if a.controlGen == gen {
			a.sendControl = nil
		}
		a.controlMu.Unlock()
	}()

	log.Printf("agent: control stream established")
	for {
		msg, err := mux.ReadControlMessage(stream)
		if err != nil {
			// Stream closed or read error — control channel is gone.
			// This is expected when the yamux session dies.
			log.Printf("agent: control stream closed: %v", err)
			return
		}
		a.dispatchControlMessage(msg)
	}
}

// dispatchControlMessage routes one control message to the target
// data stream handler based on stream ID. Agent-level messages
// (log_req) are handled before the stream registry lookup.
func (a *Agent) dispatchControlMessage(msg *mux.ControlMessage) {
	if a.Verbose {
		verboseTrace("control recv: type=%s stream_id=%d payload_len=%d",
			msg.Type, msg.StreamID, len(msg.Payload))
	}
	// Agent-level messages use stream_id=0 and are handled directly.
	switch msg.Type {
	case mux.MsgLogRequest:
		a.handleLogRequest(msg)
		return
	}

	target, ok := a.registry.Lookup(msg.StreamID)
	if !ok {
		// Stream may have closed before the message arrived. Log at debug
		// level — this is expected during session teardown.
		log.Printf("agent: control message type=%s for unknown stream %d (may have closed)", msg.Type, msg.StreamID)
		return
	}

	switch msg.Type {
	case mux.MsgResize:
		var payload mux.ResizePayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			log.Printf("agent: bad resize payload: %v", err)
			return
		}
		if a.Verbose {
			verboseTrace("resize dispatch: stream=%d cols=%d rows=%d",
				msg.StreamID, payload.Cols, payload.Rows)
		}
		// Bounds-validate dimensions to prevent PTY ioctl issues.
		// 0×0 or extreme values (e.g. 65535×65535) can cause division
		// by zero or excessive memory use in terminal emulators.
		if payload.Cols == 0 || payload.Rows == 0 || payload.Cols > 1024 || payload.Rows > 1024 {
			log.Printf("agent: rejecting unreasonable resize: %dx%d (stream=%d)", payload.Cols, payload.Rows, msg.StreamID)
			return
		}
		target.HandleResize(payload.Cols, payload.Rows)
	case mux.MsgTrace:
		var event mux.TraceEvent
		if err := json.Unmarshal(msg.Payload, &event); err != nil {
			log.Printf("agent: bad trace payload: %v", err)
			return
		}
		target.HandleTrace(event)
	default:
		log.Printf("agent: unknown control message type=%s stream=%d", msg.Type, msg.StreamID)
	}
}

// handleLogRequest responds to a server request for recent agent log
// lines. It reads from the log buffer and sends the response via the
// control stream. The request_id correlates the response.
func (a *Agent) handleLogRequest(msg *mux.ControlMessage) {
	var payload mux.LogRequestPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		log.Printf("agent: bad log request payload: %v", err)
		return
	}
	lines := int(payload.Lines)
	if lines <= 0 {
		lines = 50 // default
	}
	if lines > 200 {
		lines = 200 // cap to keep control messages small
	}
	logLines := a.logBuf.Read(lines)
	if logLines == nil {
		logLines = []string{"(no log lines captured yet)"}
	}
	resp := mux.LogResponsePayload{
		RequestID: payload.RequestID,
		Lines:     logLines,
	}
	respPayload, _ := json.Marshal(resp)
	respMsg := &mux.ControlMessage{
		Type:     mux.MsgLogResponse,
		StreamID: 0,
		Payload:  respPayload,
	}
	a.controlMu.RLock()
	send := a.sendControl
	a.controlMu.RUnlock()
	if send != nil {
		send(respMsg)
	}
}

// SendTrace sends a diagnostic trace event via the control stream.
// Non-blocking: drops if the control stream is not available. Logs a
// warning on the first drop per control stream cycle to aid diagnosis.
//
// Reserved for the agent-side diagnostic tracing feature described in
// ADR-001 ("What's Still To Come"). Currently has no callers; will be
// used by shell and data stream handlers to report PTY I/O timing,
// shell lifecycle events, and other diagnostic data.
func (a *Agent) SendTrace(streamID uint32, eventType string, durationMs int64, bytes int64, message string) {
	a.controlMu.RLock()
	send := a.sendControl
	a.controlMu.RUnlock()
	if send == nil {
		if !a.traceDropLogged.Swap(true) {
			log.Printf("agent: trace dropped (control stream unavailable): stream=%d event=%s", streamID, eventType)
		}
		return
	}
	event := mux.TraceEvent{
		TS:         time.Now().UTC().Format(time.RFC3339Nano),
		EventType:  eventType,
		DurationMs: durationMs,
		Bytes:      bytes,
		Message:    message,
	}
	send(mux.NewTraceMessage(streamID, event))
}
