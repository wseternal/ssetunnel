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
func (a *Agent) openControlStream(sess *yamux.Session) {
	cs, err := sess.OpenStream()
	if err != nil {
		log.Printf("agent: open control stream: %v", err)
		return
	}
	if _, err := cs.Write([]byte(mux.ControlStreamMarker)); err != nil {
		log.Printf("agent: write control marker: %v", err)
		cs.Close()
		return
	}
	go a.handleControlStream(cs, a.registry)
}

// handleControlStream reads control messages from the server and dispatches
// them to the appropriate data stream handler via the stream registry.
// It runs until the stream closes or an error occurs.
func (a *Agent) handleControlStream(stream net.Conn, registry *streamRegistry) {
	// Install the control sender so data streams can push trace events.
	a.controlMu.Lock()
	a.sendControl = func(msg *mux.ControlMessage) {
		if err := mux.WriteControlMessage(stream, msg); err != nil {
			log.Printf("agent: control send failed: %v", err)
		}
	}
	a.controlMu.Unlock()
	defer func() {
		a.controlMu.Lock()
		a.sendControl = nil
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
		a.dispatchControlMessage(msg, registry)
	}
}

// dispatchControlMessage routes one control message to the target
// data stream handler based on stream ID.
func (a *Agent) dispatchControlMessage(msg *mux.ControlMessage, registry *streamRegistry) {
	target, ok := registry.Lookup(msg.StreamID)
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
		target.HandleResize(payload.Cols, payload.Rows)
	case mux.MsgTrace:
		var event mux.TraceEvent
		if err := json.Unmarshal(msg.Payload, &event); err != nil {
			log.Printf("agent: bad trace payload: %v", err)
			return
		}
		target.HandleTrace(event)
	default:
		log.Printf("agent: unknown control message type: %s", msg.Type)
	}
}

// SendTrace sends a diagnostic trace event via the control stream.
// Non-blocking: silently drops if the control stream is not available.
func (a *Agent) SendTrace(streamID uint32, eventType string, durationMs int64, bytes int64, message string) {
	a.controlMu.RLock()
	send := a.sendControl
	a.controlMu.RUnlock()
	if send == nil {
		return // control stream not available
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
