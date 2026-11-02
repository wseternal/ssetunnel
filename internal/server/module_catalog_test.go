package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"github.com/wseternal/ssetunnel/internal/mux"
)

// TestModuleCatalog_SnapshotReassembly verifies the full start→chunk→commit flow.
func TestModuleCatalog_SnapshotReassembly(t *testing.T) {
	mc := NewModuleCatalog("test-epoch")

	// Build a minimal snapshot with one module.
	openapi := []byte(`openapi: "3.0.0"
info:
  title: Test Service
  version: "1.0.0"
paths: {}
`)
	entries := []moduleproto.SnapshotModuleEntry{
		{
			ModuleID:       "test-module",
			ServiceName:    "Test Service",
			ServiceVersion: "1.0.0",
			Revision:       "abc123",
			OpenAPIYAML:    openapi,
			Health:         moduleproto.HealthEntry{Status: "ready"},
		},
	}

	data, err := moduleproto.MarshalSnapshotEntries(entries)
	if err != nil {
		t.Fatalf("marshal entries: %v", err)
	}

	digest := sha256.Sum256(data)
	digestHex := hex.EncodeToString(digest[:])

	// Split into 2 chunks.
	mid := len(data) / 2
	chunks := [][]byte{data[:mid], data[mid:]}

	// 1. Start.
	startPayload, _ := json.Marshal(moduleproto.SnapshotStartPayload{
		Epoch:       "test-epoch",
		ModuleCount: 1,
		TotalSize:   int64(len(data)),
	})
	if err := mc.HandleControlMessage(&mux.ControlMessage{
		Type:    moduleproto.MsgSnapshotStart,
		Payload: startPayload,
	}); err != nil {
		t.Fatalf("snapshot start: %v", err)
	}

	// 2. Chunks.
	for i, chunk := range chunks {
		chunkPayload, _ := json.Marshal(moduleproto.SnapshotChunkPayload{
			Seq:  i,
			Data: chunk,
		})
		if err := mc.HandleControlMessage(&mux.ControlMessage{
			Type:    moduleproto.MsgSnapshotChunk,
			Payload: chunkPayload,
		}); err != nil {
			t.Fatalf("snapshot chunk %d: %v", i, err)
		}
	}

	// 3. Commit.
	commitPayload, _ := json.Marshal(moduleproto.SnapshotCommitPayload{
		Epoch:      "test-epoch",
		ChunkCount: 2,
		Digest:     digestHex,
	})
	if err := mc.HandleControlMessage(&mux.ControlMessage{
		Type:    moduleproto.MsgSnapshotCommit,
		Payload: commitPayload,
	}); err != nil {
		t.Fatalf("snapshot commit: %v", err)
	}

	// Verify catalog.
	modules := mc.ListModules()
	if len(modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(modules))
	}
	if modules[0].ModuleID != "test-module" {
		t.Errorf("module ID: got %s, want test-module", modules[0].ModuleID)
	}
	if modules[0].ServiceName != "Test Service" {
		t.Errorf("service name: got %s, want Test Service", modules[0].ServiceName)
	}
	if modules[0].Revision != "abc123" {
		t.Errorf("revision: got %s, want abc123", modules[0].Revision)
	}
	if mc.Revision() != 1 {
		t.Errorf("catalog revision: got %d, want 1", mc.Revision())
	}

	// Verify metrics.
	summary := mc.metrics.Summary()
	if summary["snapshot_count"].(int64) != 1 {
		t.Errorf("snapshot count: got %d, want 1", summary["snapshot_count"])
	}
	if summary["catalog_size"].(int64) != 1 {
		t.Errorf("catalog size: got %d, want 1", summary["catalog_size"])
	}
}

// TestModuleCatalog_DigestMismatch verifies that a bad digest is rejected.
func TestModuleCatalog_DigestMismatch(t *testing.T) {
	mc := NewModuleCatalog("test-epoch")

	startPayload, _ := json.Marshal(moduleproto.SnapshotStartPayload{
		Epoch:       "test-epoch",
		ModuleCount: 1,
		TotalSize:   4,
	})
	if err := mc.HandleControlMessage(&mux.ControlMessage{
		Type:    moduleproto.MsgSnapshotStart,
		Payload: startPayload,
	}); err != nil {
		t.Fatalf("snapshot start: %v", err)
	}

	chunkPayload, _ := json.Marshal(moduleproto.SnapshotChunkPayload{
		Seq:  0,
		Data: []byte("test"),
	})
	if err := mc.HandleControlMessage(&mux.ControlMessage{
		Type:    moduleproto.MsgSnapshotChunk,
		Payload: chunkPayload,
	}); err != nil {
		t.Fatalf("snapshot chunk: %v", err)
	}

	commitPayload, _ := json.Marshal(moduleproto.SnapshotCommitPayload{
		Epoch:      "test-epoch",
		ChunkCount: 1,
		Digest:     "bad-digest",
	})
	if err := mc.HandleControlMessage(&mux.ControlMessage{
		Type:    moduleproto.MsgSnapshotCommit,
		Payload: commitPayload,
	}); err == nil {
		t.Fatal("expected digest mismatch error, got nil")
	}
}

// TestModuleCatalog_ChunkSeqMismatch verifies that out-of-order chunks are rejected.
func TestModuleCatalog_ChunkSeqMismatch(t *testing.T) {
	mc := NewModuleCatalog("test-epoch")

	startPayload, _ := json.Marshal(moduleproto.SnapshotStartPayload{
		Epoch:       "test-epoch",
		ModuleCount: 1,
		TotalSize:   8,
	})
	if err := mc.HandleControlMessage(&mux.ControlMessage{
		Type:    moduleproto.MsgSnapshotStart,
		Payload: startPayload,
	}); err != nil {
		t.Fatalf("snapshot start: %v", err)
	}

	// Send chunk with wrong seq (expecting 0, sending 1).
	chunkPayload, _ := json.Marshal(moduleproto.SnapshotChunkPayload{
		Seq:  1,
		Data: []byte("test"),
	})
	if err := mc.HandleControlMessage(&mux.ControlMessage{
		Type:    moduleproto.MsgSnapshotChunk,
		Payload: chunkPayload,
	}); err == nil {
		t.Fatal("expected seq mismatch error, got nil")
	}
}

// TestModuleCatalog_EpochMismatch verifies that a wrong epoch is rejected.
func TestModuleCatalog_EpochMismatch(t *testing.T) {
	mc := NewModuleCatalog("test-epoch")

	startPayload, _ := json.Marshal(moduleproto.SnapshotStartPayload{
		Epoch:       "wrong-epoch",
		ModuleCount: 1,
		TotalSize:   0,
	})
	if err := mc.HandleControlMessage(&mux.ControlMessage{
		Type:    moduleproto.MsgSnapshotStart,
		Payload: startPayload,
	}); err == nil {
		t.Fatal("expected epoch mismatch error, got nil")
	}
}

// TestModuleCatalog_LookupModule verifies module lookup by ID.
func TestModuleCatalog_LookupModule(t *testing.T) {
	mc := NewModuleCatalog("test-epoch")

	// Before any snapshot, lookup should return nil.
	if info := mc.GetModule("test-module"); info != nil {
		t.Fatal("expected nil before snapshot")
	}

	// Commit a snapshot.
	openapi := []byte(`openapi: "3.0.0"
info:
  title: Test
  version: "1.0.0"
paths: {}
`)
	entries := []moduleproto.SnapshotModuleEntry{
		{
			ModuleID:       "test-module",
			ServiceName:    "Test",
			ServiceVersion: "1.0.0",
			Revision:       "rev1",
			OpenAPIYAML:    openapi,
			Health:         moduleproto.HealthEntry{Status: "ready"},
		},
	}
	data, _ := moduleproto.MarshalSnapshotEntries(entries)
	digest := sha256.Sum256(data)
	digestHex := hex.EncodeToString(digest[:])

	startPayload, _ := json.Marshal(moduleproto.SnapshotStartPayload{
		Epoch:       "test-epoch",
		ModuleCount: 1,
		TotalSize:   int64(len(data)),
	})
	mc.HandleControlMessage(&mux.ControlMessage{
		Type:    moduleproto.MsgSnapshotStart,
		Payload: startPayload,
	})

	chunkPayload, _ := json.Marshal(moduleproto.SnapshotChunkPayload{
		Seq:  0,
		Data: data,
	})
	mc.HandleControlMessage(&mux.ControlMessage{
		Type:    moduleproto.MsgSnapshotChunk,
		Payload: chunkPayload,
	})

	commitPayload, _ := json.Marshal(moduleproto.SnapshotCommitPayload{
		Epoch:      "test-epoch",
		ChunkCount: 1,
		Digest:     digestHex,
	})
	mc.HandleControlMessage(&mux.ControlMessage{
		Type:    moduleproto.MsgSnapshotCommit,
		Payload: commitPayload,
	})

	// Lookup should now find the module.
	info := mc.GetModule("test-module")
	if info == nil {
		t.Fatal("expected module info, got nil")
	}
	if info.ModuleID != "test-module" {
		t.Errorf("module ID: got %s, want test-module", info.ModuleID)
	}

	// Lookup unknown module.
	if info := mc.GetModule("unknown"); info != nil {
		t.Error("expected nil for unknown module")
	}
}

// TestModuleCatalog_MultipleSnapshots verifies revision incrementing.
func TestModuleCatalog_MultipleSnapshots(t *testing.T) {
	mc := NewModuleCatalog("test-epoch")

	openapi := []byte(`openapi: "3.0.0"
info:
  title: Test
  version: "1.0.0"
paths: {}
`)

	for i := 0; i < 3; i++ {
		entries := []moduleproto.SnapshotModuleEntry{
			{
				ModuleID:       "test-module",
				ServiceName:    "Test",
				ServiceVersion: "1.0.0",
				Revision:       "rev",
				OpenAPIYAML:    openapi,
				Health:         moduleproto.HealthEntry{Status: "ready"},
			},
		}
		data, _ := moduleproto.MarshalSnapshotEntries(entries)
		digest := sha256.Sum256(data)
		digestHex := hex.EncodeToString(digest[:])

		startPayload, _ := json.Marshal(moduleproto.SnapshotStartPayload{
			Epoch:       "test-epoch",
			ModuleCount: 1,
			TotalSize:   int64(len(data)),
		})
		mc.HandleControlMessage(&mux.ControlMessage{
			Type:    moduleproto.MsgSnapshotStart,
			Payload: startPayload,
		})

		chunkPayload, _ := json.Marshal(moduleproto.SnapshotChunkPayload{
			Seq:  0,
			Data: data,
		})
		mc.HandleControlMessage(&mux.ControlMessage{
			Type:    moduleproto.MsgSnapshotChunk,
			Payload: chunkPayload,
		})

		commitPayload, _ := json.Marshal(moduleproto.SnapshotCommitPayload{
			Epoch:      "test-epoch",
			ChunkCount: 1,
			Digest:     digestHex,
		})
		mc.HandleControlMessage(&mux.ControlMessage{
			Type:    moduleproto.MsgSnapshotCommit,
			Payload: commitPayload,
		})
	}

	if mc.Revision() != 3 {
		t.Errorf("revision: got %d, want 3", mc.Revision())
	}
}
