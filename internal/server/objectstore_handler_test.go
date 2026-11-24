package server

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/metrics"
	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"github.com/wseternal/ssetunnel/internal/mux"
)

func TestExpireAssembliesLocked(t *testing.T) {
	data := bytes.Repeat([]byte{0x11}, moduleproto.ObjectStoreChunkSize*2)
	chunks, err := moduleproto.NewObjectStoreDataChunks("req-exp", moduleproto.ObjectStorePut, "com.example.store", "blob.bin", data)
	if err != nil {
		t.Fatalf("chunks: %v", err)
	}
	asm, err := moduleproto.NewObjectStoreAssembler(chunks[0])
	if err != nil {
		t.Fatalf("assembler: %v", err)
	}

	key := objectAssemblyKey{moduleID: "com.example.store", requestID: "req-exp"}
	h := &ObjectStoreHandler{
		assemblies: map[objectAssemblyKey]*moduleproto.ObjectStoreAssembler{key: asm},
		assemblyAt: map[objectAssemblyKey]time.Time{key: time.Now().Add(-objectStoreAssemblyTTL - time.Minute)},
	}
	h.expireAssembliesLocked(time.Now())

	if len(h.assemblies) != 0 || len(h.assemblyAt) != 0 {
		t.Fatalf("expired assembly retained: assemblies=%d assemblyAt=%d", len(h.assemblies), len(h.assemblyAt))
	}
}

func TestHandlePutScopesAssembliesByModule(t *testing.T) {
	data := bytes.Repeat([]byte{0x22}, moduleproto.ObjectStoreChunkSize*2)
	chunksA, err := moduleproto.NewObjectStoreDataChunks("shared", moduleproto.ObjectStorePut, "com.example.a", "blob.bin", data)
	if err != nil {
		t.Fatalf("chunks A: %v", err)
	}
	chunksB, err := moduleproto.NewObjectStoreDataChunks("shared", moduleproto.ObjectStorePut, "com.example.b", "blob.bin", data)
	if err != nil {
		t.Fatalf("chunks B: %v", err)
	}

	h := newObjectStoreHandler(nil, nil, nil)
	defer h.Close()
	h.handleMessage(objectStoreRequestMessage(t, chunksA[0]))
	h.handleMessage(objectStoreRequestMessage(t, chunksB[0]))

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.assemblies) != 2 {
		t.Fatalf("assemblies = %d, want 2 distinct module keys", len(h.assemblies))
	}
	for _, moduleID := range []string{"com.example.a", "com.example.b"} {
		key := objectAssemblyKey{moduleID: moduleID, requestID: "shared"}
		if h.assemblies[key] == nil {
			t.Fatalf("missing assembly for %s", moduleID)
		}
	}
}

func objectStoreRequestMessage(t *testing.T, msg *moduleproto.ObjectStoreMessage) *mux.ControlMessage {
	t.Helper()
	payload, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal object store request: %v", err)
	}
	return &mux.ControlMessage{Type: moduleproto.MsgObjectStoreReq, Payload: payload}
}

// TestObjectStoreHandlerExecuteRoundTrip drives the handler through a
// real filesystem backend: one chunked put, one get, and the terminal
// responses the module would receive.
func TestObjectStoreHandlerExecuteRoundTrip(t *testing.T) {
	store, err := NewFileObjectStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file object store: %v", err)
	}
	defer store.Close()

	responses := make(chan *moduleproto.ObjectStoreMessage, 8)
	h := newObjectStoreHandler(store, func(m *mux.ControlMessage) error {
		var resp moduleproto.ObjectStoreMessage
		if err := json.Unmarshal(m.Payload, &resp); err != nil {
			responses <- nil
			return nil
		}
		responses <- &resp
		return nil
	}, metrics.NewObjectStoreMetrics())
	defer h.Close()

	data := []byte("hello object store")
	chunks, err := moduleproto.NewObjectStoreDataChunks("req-put", moduleproto.ObjectStorePut, "com.example.store", "blob.bin", data)
	if err != nil {
		t.Fatalf("chunks: %v", err)
	}
	for _, chunk := range chunks {
		h.handleMessage(objectStoreRequestMessage(t, chunk))
	}
	putResp := awaitObjectStoreResponse(t, responses)
	if putResp == nil || putResp.ErrorCode != "" || putResp.TotalSize != int64(len(data)) {
		t.Fatalf("put response = %+v, want a final success for %d bytes", putResp, len(data))
	}

	h.handleMessage(objectStoreRequestMessage(t, moduleproto.NewObjectStoreGet("req-get", "com.example.store", "blob.bin")))
	getResp := awaitObjectStoreResponse(t, responses)
	if getResp == nil || getResp.ErrorCode != "" || !bytes.Equal(getResp.Data, data) {
		t.Fatalf("get response = %+v, want the stored bytes", getResp)
	}

	// A get with non-canonical metadata is rejected before any disk work.
	bad := moduleproto.NewObjectStoreGet("req-bad", "com.example.store", "blob.bin")
	bad.Sequence = 1
	h.handleMessage(objectStoreRequestMessage(t, bad))
	badResp := awaitObjectStoreResponse(t, responses)
	if badResp == nil || badResp.RequestID != "req-bad" || badResp.ErrorCode != moduleproto.ObjectStoreErrInvalidRequest {
		t.Fatalf("bad get response = %+v, want invalid_request", badResp)
	}
	if queued := len(h.queue); queued != 0 {
		t.Fatalf("non-canonical get queued %d jobs, want 0", queued)
	}
}

// awaitObjectStoreResponse waits for one handler response.
func awaitObjectStoreResponse(t *testing.T, responses <-chan *moduleproto.ObjectStoreMessage) *moduleproto.ObjectStoreMessage {
	t.Helper()
	select {
	case resp := <-responses:
		return resp
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an object store response")
		return nil
	}
}
