package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/wseternal/ssetunnel/internal/metrics"
	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"github.com/wseternal/ssetunnel/internal/mux"
)

// ModuleStreamHandler manages module invocation streams on the server side.
// It sends module_open requests to agents and correlates their reverse streams
// with pending HTTP invocations.
type ModuleStreamHandler struct {
	mu sync.Mutex

	// Session for this handler.
	session *Session

	// Metrics collector (C15 fix: wired).
	metrics *metrics.ModuleMetrics

	// Pending invocations waiting for agent to open stream.
	pending map[string]*PendingInvocation

	// Active invocations.
	active map[string]*ActiveInvocation
}

// PendingInvocation is an invocation waiting for the agent to open a stream.
type PendingInvocation struct {
	ID            string
	ModuleID      string
	Revision      string
	CorrelationID string
	ResponseCh    chan net.Conn
	Done          chan struct{} // C8 fix: signal cancellation without closing ResponseCh
	CreatedAt     time.Time
}

// ActiveInvocation is an invocation with an established stream.
type ActiveInvocation struct {
	ID        string
	ModuleID  string
	Stream    net.Conn
	StartedAt time.Time
}

// NewModuleStreamHandler creates a handler for the given session.
func NewModuleStreamHandler(session *Session) *ModuleStreamHandler {
	return &ModuleStreamHandler{
		session: session,
		metrics: metrics.NewModuleMetrics(100),
		pending: make(map[string]*PendingInvocation),
		active:  make(map[string]*ActiveInvocation),
	}
}

// Metrics returns the module metrics collector.
func (h *ModuleStreamHandler) Metrics() *metrics.ModuleMetrics {
	return h.metrics
}

// RequestInvocation sends a module_open to the agent and waits for the stream.
// Returns the stream and the invocation ID for later cleanup.
func (h *ModuleStreamHandler) RequestInvocation(ctx context.Context, moduleID, revision string, req *http.Request) (net.Conn, string, error) {
	// Generate invocation ID.
	invocationID := generateInvocationID()
	correlationID := req.Header.Get("X-Correlation-ID")

	// Create pending invocation.
	pending := &PendingInvocation{
		ID:            invocationID,
		ModuleID:      moduleID,
		Revision:      revision,
		CorrelationID: correlationID,
		ResponseCh:    make(chan net.Conn, 1),
		Done:          make(chan struct{}), // C8 fix
		CreatedAt:     time.Now(),
	}

	h.mu.Lock()
	if len(h.pending) >= moduleproto.MaxPendingOpens {
		h.mu.Unlock()
		h.metrics.RecordError("transport")
		return nil, "", fmt.Errorf("too many pending invocations")
	}
	h.pending[invocationID] = pending
	h.mu.Unlock()
	h.metrics.RecordPendingOpen()

	// Send module_open to agent.
	payload, _ := json.Marshal(moduleproto.ModuleOpenPayload{
		InvocationID:  invocationID,
		ModuleID:      moduleID,
		Revision:      revision,
		Method:        req.Method,
		Path:          req.URL.Path,
		CorrelationID: correlationID,
	})

	if err := h.session.SendControl(&mux.ControlMessage{
		Type:    moduleproto.MsgModuleOpen,
		Payload: payload,
	}); err != nil {
		h.mu.Lock()
		delete(h.pending, invocationID)
		h.mu.Unlock()
		h.metrics.RecordPendingClose()
		h.metrics.RecordError("transport")
		return nil, "", fmt.Errorf("send module_open: %w", err)
	}
	h.metrics.RecordInvocation()

	// Wait for agent to open stream (with deadline).
	deadline := time.After(time.Duration(moduleproto.EstablishmentDeadline) * time.Second)
	select {
	case stream := <-pending.ResponseCh:
		// Move to active.
		h.mu.Lock()
		delete(h.pending, invocationID)
		h.active[invocationID] = &ActiveInvocation{
			ID:        invocationID,
			ModuleID:  moduleID,
			Stream:    stream,
			StartedAt: time.Now(),
		}
		h.mu.Unlock()
		h.metrics.RecordPendingClose()
		return stream, invocationID, nil

	case <-pending.Done:
		// C8 fix: cancelled via Done channel (not closed ResponseCh).
		h.mu.Lock()
		delete(h.pending, invocationID)
		h.mu.Unlock()
		h.metrics.RecordPendingClose()
		h.metrics.RecordInvocationComplete(false, true)
		return nil, "", fmt.Errorf("invocation cancelled")

	case <-deadline:
		h.mu.Lock()
		delete(h.pending, invocationID)
		h.mu.Unlock()
		h.metrics.RecordPendingClose()
		h.metrics.RecordInvocationComplete(true, false)
		return nil, "", fmt.Errorf("establishment timeout")

	case <-ctx.Done():
		h.mu.Lock()
		delete(h.pending, invocationID)
		h.mu.Unlock()
		h.metrics.RecordPendingClose()
		h.metrics.RecordInvocationComplete(true, false)
		return nil, "", ctx.Err()
	}
}

// DeliverStream delivers an already-parsed module stream to a pending invocation.
// The invocation ID was extracted from the marker by acceptModuleStreams.
func (h *ModuleStreamHandler) DeliverStream(stream net.Conn, invocationID string) error {
	h.mu.Lock()
	pending, ok := h.pending[invocationID]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("unknown invocation ID: %s", invocationID)
	}
	delete(h.pending, invocationID)
	h.mu.Unlock()

	// Signal the waiting RequestInvocation (C8 fix: use non-blocking send).
	select {
	case pending.ResponseCh <- stream:
		return nil
	case <-pending.Done:
		// Invocation was cancelled.
		stream.Close()
		return fmt.Errorf("invocation cancelled before delivery")
	default:
		return fmt.Errorf("invocation already satisfied")
	}
}

// CompleteInvocation marks an invocation as complete and cleans up.
func (h *ModuleStreamHandler) CompleteInvocation(invocationID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if inv, ok := h.active[invocationID]; ok {
		inv.Stream.Close()
		delete(h.active, invocationID)
		h.metrics.RecordInvocationComplete(false, false)
	}
}

// CancelInvocation cancels a pending or active invocation.
func (h *ModuleStreamHandler) CancelInvocation(invocationID string, reason string) {
	h.mu.Lock()

	// Check pending (C8 fix: signal Done instead of closing ResponseCh).
	if pending, ok := h.pending[invocationID]; ok {
		close(pending.Done) // Signal cancellation safely.
		delete(h.pending, invocationID)
		h.metrics.RecordPendingClose()
		h.metrics.RecordInvocationComplete(false, true)
	}

	// Check active.
	if inv, ok := h.active[invocationID]; ok {
		inv.Stream.Close()
		delete(h.active, invocationID)
		h.metrics.RecordInvocationComplete(false, true)
	}
	h.mu.Unlock()

	// Send module_close to agent.
	payload, _ := json.Marshal(moduleproto.ModuleClosePayload{
		InvocationID: invocationID,
		Reason:       reason,
	})
	h.session.SendControl(&mux.ControlMessage{
		Type:    moduleproto.MsgModuleClose,
		Payload: payload,
	})
}

// Close cancels all pending and active invocations.
func (h *ModuleStreamHandler) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for id, pending := range h.pending {
		close(pending.Done) // C8 fix: signal Done.
		delete(h.pending, id)
	}

	for id, inv := range h.active {
		inv.Stream.Close()
		delete(h.active, id)
	}
}

// ActiveCount returns the number of active invocations.
func (h *ModuleStreamHandler) ActiveCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.active)
}

// PendingCount returns the number of pending invocations.
func (h *ModuleStreamHandler) PendingCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pending)
}

// generateInvocationID creates a unique invocation ID.
func generateInvocationID() string {
	var randBytes [16]byte
	if _, err := rand.Read(randBytes[:]); err != nil {
		// Fallback to timestamp-based ID on crypto failure.
		return fmt.Sprintf("inv-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(randBytes[:])
}
