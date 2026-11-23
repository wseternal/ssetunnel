package server

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

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
