package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"github.com/wseternal/ssetunnel/internal/mux"
)

func TestObjectStoreForwarderRejectsClosedGeneration(t *testing.T) {
	f := newObjectStoreForwarder()
	f.FailGeneration(7, errObjectStoreStreamClosed)

	sent := false
	_, err := f.Forward(context.Background(), moduleproto.ObjectStoreMessage{
		RequestID: "req-closed",
		Op:        moduleproto.ObjectStoreGet,
		ModuleID:  "com.example.store",
		Key:       "blob.bin",
	}, func(*mux.ControlMessage) error { sent = true; return nil }, 7)
	if !errors.Is(err, errObjectStoreStreamClosed) {
		t.Fatalf("Forward error = %v, want %v", err, errObjectStoreStreamClosed)
	}
	if sent {
		t.Fatal("Forward sent a request on a closed generation")
	}
}

func TestObjectStoreForwarderRejectsDuplicateRequestID(t *testing.T) {
	f := newObjectStoreForwarder()
	f.pending["dup"] = &objectStorePending{requestID: "dup", done: make(chan struct{})}

	sent := false
	_, err := f.Forward(context.Background(), moduleproto.ObjectStoreMessage{
		RequestID: "dup",
		Op:        moduleproto.ObjectStoreGet,
		ModuleID:  "com.example.store",
		Key:       "blob.bin",
	}, func(*mux.ControlMessage) error { sent = true; return nil }, 1)
	if err == nil || !strings.Contains(err.Error(), "duplicate request_id") {
		t.Fatalf("Forward error = %v, want duplicate request_id", err)
	}
	if sent {
		t.Fatal("Forward sent a duplicate request")
	}
}

func TestObjectStoreForwarderIgnoresMismatchedModule(t *testing.T) {
	f := newObjectStoreForwarder()
	p := &objectStorePending{
		requestID: "req-module",
		gen:       1,
		op:        moduleproto.ObjectStoreGet,
		moduleID:  "com.example.a",
		key:       "blob.bin",
		done:      make(chan struct{}),
	}
	f.pending[p.requestID] = p

	payload, err := json.Marshal(moduleproto.ObjectStoreMessage{
		RequestID: p.requestID,
		Op:        p.op,
		ModuleID:  "com.example.b",
		Final:     true,
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	f.HandleResponse(&mux.ControlMessage{Type: moduleproto.MsgObjectStoreResp, Payload: payload}, p.gen)

	if p.finished {
		t.Fatal("mismatched module response finished the pending request")
	}
	if len(f.pending) != 1 {
		t.Fatalf("pending entries = %d, want 1", len(f.pending))
	}
}

func TestObjectStoreForwarderAcceptsErrorWithEmptyModule(t *testing.T) {
	f := newObjectStoreForwarder()
	p := &objectStorePending{
		requestID: "req-error",
		gen:       1,
		op:        moduleproto.ObjectStoreGet,
		moduleID:  "com.example.a",
		key:       "missing.bin",
		done:      make(chan struct{}),
	}
	f.pending[p.requestID] = p

	payload, err := json.Marshal(moduleproto.NewObjectStoreError(
		p.requestID, p.op, moduleproto.ObjectStoreErrNotFound, "object not found"))
	if err != nil {
		t.Fatalf("marshal error response: %v", err)
	}
	f.HandleResponse(&mux.ControlMessage{Type: moduleproto.MsgObjectStoreResp, Payload: payload}, p.gen)

	if !p.finished {
		t.Fatal("error response did not finish the pending request")
	}
	if p.err == nil || !strings.Contains(p.err.Error(), moduleproto.ObjectStoreErrNotFound) {
		t.Fatalf("pending error = %v, want not_found", p.err)
	}
}

// TestObjectStoreForwarderFailsOnSendError proves a failed control write
// ends the operation at once instead of waiting for the request timeout
// or for a generation cleanup.
func TestObjectStoreForwarderFailsOnSendError(t *testing.T) {
	f := newObjectStoreForwarder()
	writeErr := errors.New("control stream write failed")

	_, err := f.Forward(context.Background(), moduleproto.ObjectStoreMessage{
		RequestID: "req-send",
		Op:        moduleproto.ObjectStorePut,
		ModuleID:  "com.example.store",
		Key:       "blob.bin",
		Data:      []byte("x"),
	}, func(*mux.ControlMessage) error { return writeErr }, 1)
	if !errors.Is(err, writeErr) {
		t.Fatalf("Forward error = %v, want %v", err, writeErr)
	}
	if len(f.pending) != 0 {
		t.Fatalf("pending entries = %d, want 0 after a failed send", len(f.pending))
	}
}

func TestInitForwardersIsSingleShot(t *testing.T) {
	a := &Agent{}
	a.initForwarders()
	dataStore := a.dataStoreFwd
	objectStore := a.objectStoreFwd

	a.initForwarders()
	if a.dataStoreFwd != dataStore || a.objectStoreFwd != objectStore {
		t.Fatal("initForwarders replaced an existing forwarder")
	}
}
