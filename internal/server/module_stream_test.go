package server

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/moduleproto"
)

func TestDeliverStream_CorrelatesPending(t *testing.T) {
	h := NewModuleStreamHandler(NewSession("test"))

	_, w := net.Pipe()
	defer w.Close()

	h.mu.Lock()
	h.pending["inv-1"] = &PendingInvocation{
		ID:         "inv-1",
		ModuleID:   "com.test.mod",
		ResponseCh: make(chan net.Conn, 1),
		Done:       make(chan struct{}),
		CreatedAt:  time.Now(),
	}
	h.mu.Unlock()

	if err := h.DeliverStream(w, "inv-1"); err != nil {
		t.Fatalf("DeliverStream: %v", err)
	}

	if h.ActiveCount() != 0 {
		t.Errorf("ActiveCount = %d, want 0 (DeliverStream sends to ResponseCh, caller moves to active)", h.ActiveCount())
	}
	if h.PendingCount() != 0 {
		t.Errorf("PendingCount = %d, want 0", h.PendingCount())
	}
}

func TestDeliverStream_RejectsUnknownID(t *testing.T) {
	h := NewModuleStreamHandler(NewSession("test"))

	_, w := net.Pipe()
	defer w.Close()

	err := h.DeliverStream(w, "unknown-id")
	if err == nil {
		t.Fatal("DeliverStream should fail for unknown invocation ID")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error = %q, want to contain 'unknown'", err.Error())
	}
}

func TestDeliverStream_CancelledBeforeDelivery(t *testing.T) {
	h := NewModuleStreamHandler(NewSession("test"))

	_, w := net.Pipe()
	defer w.Close()

	done := make(chan struct{})
	close(done) // already cancelled

	h.mu.Lock()
	h.pending["inv-2"] = &PendingInvocation{
		ID:         "inv-2",
		ModuleID:   "com.test.mod",
		ResponseCh: make(chan net.Conn, 1),
		Done:       done,
		CreatedAt:  time.Now(),
	}
	h.mu.Unlock()

	err := h.DeliverStream(w, "inv-2")
	if err == nil {
		t.Fatal("DeliverStream should fail for cancelled invocation")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("error = %q, want 'cancelled'", err.Error())
	}
}

func TestCancelInvocation_ClosesPendingDone(t *testing.T) {
	h := NewModuleStreamHandler(NewSession("test"))

	done := make(chan struct{})
	h.mu.Lock()
	h.pending["inv-3"] = &PendingInvocation{
		ID:         "inv-3",
		ModuleID:   "com.test.mod",
		ResponseCh: make(chan net.Conn, 1),
		Done:       done,
		CreatedAt:  time.Now(),
	}
	h.mu.Unlock()

	h.CancelInvocation("inv-3", "test cancel")

	select {
	case <-done:
		// OK — Done was closed
	case <-time.After(time.Second):
		t.Fatal("Done channel not closed after cancel")
	}

	if h.PendingCount() != 0 {
		t.Errorf("PendingCount = %d, want 0", h.PendingCount())
	}
}

func TestCancelInvocation_ClosesActiveStream(t *testing.T) {
	h := NewModuleStreamHandler(NewSession("test"))

	r, w := net.Pipe()
	defer r.Close()

	h.mu.Lock()
	h.active["inv-4"] = &ActiveInvocation{
		ID:        "inv-4",
		ModuleID:  "com.test.mod",
		Stream:    w,
		StartedAt: time.Now(),
	}
	h.mu.Unlock()

	h.CancelInvocation("inv-4", "test cancel")

	if h.ActiveCount() != 0 {
		t.Errorf("ActiveCount = %d, want 0", h.ActiveCount())
	}
}

func TestClose_CleansUpAll(t *testing.T) {
	h := NewModuleStreamHandler(NewSession("test"))

	_, w1 := net.Pipe()
	defer w1.Close()
	_, w2 := net.Pipe()
	defer w2.Close()

	h.mu.Lock()
	h.pending["inv-5"] = &PendingInvocation{
		ID:         "inv-5",
		ModuleID:   "com.test.mod",
		ResponseCh: make(chan net.Conn, 1),
		Done:       make(chan struct{}),
		CreatedAt:  time.Now(),
	}
	h.active["inv-6"] = &ActiveInvocation{
		ID:        "inv-6",
		ModuleID:  "com.test.mod",
		Stream:    w2,
		StartedAt: time.Now(),
	}
	h.mu.Unlock()

	h.Close()

	if h.PendingCount() != 0 {
		t.Errorf("PendingCount = %d, want 0", h.PendingCount())
	}
	if h.ActiveCount() != 0 {
		t.Errorf("ActiveCount = %d, want 0", h.ActiveCount())
	}
}

func TestRequestInvocation_LimitEnforcement(t *testing.T) {
	sess := NewSession("test")
	r, w := net.Pipe()
	defer r.Close()
	defer w.Close()
	sess.SetControlStream(w)

	h := NewModuleStreamHandler(sess)

	// Fill pending to max.
	h.mu.Lock()
	for i := 0; i < moduleproto.MaxPendingOpens; i++ {
		id := generateInvocationID()
		h.pending[id] = &PendingInvocation{
			ID:         id,
			ModuleID:   "com.test.mod",
			ResponseCh: make(chan net.Conn, 1),
			Done:       make(chan struct{}),
			CreatedAt:  time.Now(),
		}
	}
	h.mu.Unlock()

	// Next request should fail.
	req, _ := http.NewRequest("GET", "/test", nil)
	_, _, err := h.RequestInvocation(context.Background(), "com.test.mod", "rev-1", req)
	if err == nil {
		t.Fatal("RequestInvocation should fail when pending is full")
	}
	if !strings.Contains(err.Error(), "too many pending") {
		t.Errorf("error = %q, want 'too many pending'", err.Error())
	}
}

func TestRequestInvocation_ContextCancellation(t *testing.T) {
	sess := NewSession("test")
	r, w := net.Pipe()
	defer r.Close()
	defer w.Close()
	sess.SetControlStream(w)

	h := NewModuleStreamHandler(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req, _ := http.NewRequest("GET", "/test", nil)
	_, _, err := h.RequestInvocation(ctx, "com.test.mod", "rev-1", req)
	if err == nil {
		t.Fatal("RequestInvocation should fail on context cancellation")
	}
}

func TestCompleteInvocation_RemovesActive(t *testing.T) {
	h := NewModuleStreamHandler(NewSession("test"))

	_, w := net.Pipe()
	defer w.Close()

	h.mu.Lock()
	h.active["inv-7"] = &ActiveInvocation{
		ID:        "inv-7",
		ModuleID:  "com.test.mod",
		Stream:    w,
		StartedAt: time.Now(),
	}
	h.mu.Unlock()

	h.CompleteInvocation("inv-7")

	if h.ActiveCount() != 0 {
		t.Errorf("ActiveCount = %d, want 0", h.ActiveCount())
	}
}
