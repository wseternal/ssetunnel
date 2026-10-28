//go:build !windows

package agent

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/wseternal/ssetunnel/internal/mux"
)

// mockStream implements RegisteredStream for testing.
type mockStream struct {
	mu           sync.Mutex
	resizeEvents []resizeEvent
	traceEvents  []mux.TraceEvent
}

type resizeEvent struct {
	cols, rows uint16
}

func (m *mockStream) HandleResize(cols, rows uint16) {
	m.mu.Lock()
	m.resizeEvents = append(m.resizeEvents, resizeEvent{cols, rows})
	m.mu.Unlock()
}

func (m *mockStream) HandleTrace(event mux.TraceEvent) {
	m.mu.Lock()
	m.traceEvents = append(m.traceEvents, event)
	m.mu.Unlock()
}

// TestStreamRegistry_RegisterLookup verifies basic register→lookup flow.
func TestStreamRegistry_RegisterLookup(t *testing.T) {
	r := newStreamRegistry()
	ms := &mockStream{}
	r.Register(42, ms)
	got, ok := r.Lookup(42)
	if !ok {
		t.Fatal("Lookup(42) returned false, want true")
	}
	if got != ms {
		t.Error("Lookup returned wrong stream")
	}
}

// TestStreamRegistry_Unregister verifies that unregister removes the entry.
func TestStreamRegistry_Unregister(t *testing.T) {
	r := newStreamRegistry()
	r.Register(1, &mockStream{})
	r.Unregister(1)
	_, ok := r.Lookup(1)
	if ok {
		t.Error("Lookup(1) returned true after Unregister")
	}
}

// TestStreamRegistry_LookupUnknown verifies that looking up an unknown
// ID returns false.
func TestStreamRegistry_LookupUnknown(t *testing.T) {
	r := newStreamRegistry()
	_, ok := r.Lookup(999)
	if ok {
		t.Error("Lookup(999) returned true for empty registry")
	}
}

// TestStreamRegistry_ConcurrentAccess verifies thread safety under
// concurrent register/unregister/lookup operations.
func TestStreamRegistry_ConcurrentAccess(t *testing.T) {
	r := newStreamRegistry()
	const goroutines = 50
	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id uint32) {
			defer wg.Done()
			ms := &mockStream{}
			for j := 0; j < iterations; j++ {
				r.Register(id, ms)
				r.Lookup(id)
				r.Unregister(id)
			}
		}(uint32(i))
	}
	wg.Wait()
	// No panic or data race = pass.
}

// TestDispatchControlMessage_Resize verifies that a resize message is
// routed to the correct stream handler with the correct payload.
func TestDispatchControlMessage_Resize(t *testing.T) {
	a := &Agent{registry: newStreamRegistry()}
	ms := &mockStream{}
	a.registry.Register(42, ms)

	payload, _ := json.Marshal(mux.ResizePayload{Cols: 120, Rows: 40})
	msg := &mux.ControlMessage{
		Type:     mux.MsgResize,
		StreamID: 42,
		Payload:  payload,
	}
	a.dispatchControlMessage(msg, a.registry)

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.resizeEvents) != 1 {
		t.Fatalf("resize events = %d, want 1", len(ms.resizeEvents))
	}
	if ms.resizeEvents[0].cols != 120 || ms.resizeEvents[0].rows != 40 {
		t.Errorf("resize = %+v, want {120, 40}", ms.resizeEvents[0])
	}
}

// TestDispatchControlMessage_UnknownStreamID verifies that a message
// for an unknown stream ID is logged and does not panic.
func TestDispatchControlMessage_UnknownStreamID(t *testing.T) {
	a := &Agent{registry: newStreamRegistry()}
	payload, _ := json.Marshal(mux.ResizePayload{Cols: 80, Rows: 24})
	msg := &mux.ControlMessage{
		Type:     mux.MsgResize,
		StreamID: 999,
		Payload:  payload,
	}
	// Must not panic.
	a.dispatchControlMessage(msg, a.registry)
}

// TestDispatchControlMessage_UnknownType verifies that an unknown
// message type is logged and does not panic.
func TestDispatchControlMessage_UnknownType(t *testing.T) {
	a := &Agent{registry: newStreamRegistry()}
	ms := &mockStream{}
	a.registry.Register(1, ms)

	msg := &mux.ControlMessage{
		Type:     "unknown_type",
		StreamID: 1,
		Payload:  json.RawMessage(`{}`),
	}
	// Must not panic.
	a.dispatchControlMessage(msg, a.registry)

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.resizeEvents) != 0 {
		t.Error("unknown type should not trigger resize")
	}
	if len(ms.traceEvents) != 0 {
		t.Error("unknown type should not trigger trace")
	}
}

// TestDispatchControlMessage_Trace verifies trace message routing.
func TestDispatchControlMessage_Trace(t *testing.T) {
	a := &Agent{registry: newStreamRegistry()}
	ms := &mockStream{}
	a.registry.Register(5, ms)

	event := mux.TraceEvent{EventType: "pty_read", Bytes: 512}
	payload, _ := json.Marshal(event)
	msg := &mux.ControlMessage{
		Type:     mux.MsgTrace,
		StreamID: 5,
		Payload:  payload,
	}
	a.dispatchControlMessage(msg, a.registry)

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.traceEvents) != 1 {
		t.Fatalf("trace events = %d, want 1", len(ms.traceEvents))
	}
	if ms.traceEvents[0].EventType != "pty_read" {
		t.Errorf("event_type = %q, want pty_read", ms.traceEvents[0].EventType)
	}
}

// TestDispatchControlMessage_BadResizePayload verifies that malformed
// JSON in a resize payload is handled gracefully.
func TestDispatchControlMessage_BadResizePayload(t *testing.T) {
	a := &Agent{registry: newStreamRegistry()}
	ms := &mockStream{}
	a.registry.Register(1, ms)

	msg := &mux.ControlMessage{
		Type:     mux.MsgResize,
		StreamID: 1,
		Payload:  json.RawMessage(`{not valid`),
	}
	// Must not panic.
	a.dispatchControlMessage(msg, a.registry)

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.resizeEvents) != 0 {
		t.Error("bad payload should not trigger resize")
	}
}
