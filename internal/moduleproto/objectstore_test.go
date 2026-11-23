package moduleproto

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/wseternal/ssetunnel/internal/mux"
	"github.com/wseternal/ssetunnel/sdk"
)

func TestObjectStoreChunkCount(t *testing.T) {
	cases := []struct {
		size int
		want int
	}{
		{0, 1},
		{1, 1},
		{ObjectStoreChunkSize, 1},
		{ObjectStoreChunkSize + 1, 2},
		{sdk.MaxObjectSize, sdk.MaxObjectSize / ObjectStoreChunkSize},
	}
	for _, c := range cases {
		if got := ObjectStoreChunkCount(c.size); got != c.want {
			t.Fatalf("ObjectStoreChunkCount(%d) = %d, want %d", c.size, got, c.want)
		}
	}
}

// TestObjectStoreMaxChunkFrameSize proves that a maximum-size raw chunk
// still fits one control frame. It records the largest frame.
func TestObjectStoreMaxChunkFrameSize(t *testing.T) {
	data := bytes.Repeat([]byte{0xA5}, sdk.MaxObjectSize)
	msgs, err := NewObjectStoreDataChunks("req-1", ObjectStorePut, "com.example.store", "blob.bin", data)
	if err != nil {
		t.Fatalf("fragment: %v", err)
	}
	if len(msgs) != 128 {
		t.Fatalf("chunk count = %d, want 128", len(msgs))
	}

	largest := 0
	for _, msg := range msgs {
		payload, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal chunk: %v", err)
		}
		cm := &mux.ControlMessage{Type: MsgObjectStoreReq, StreamID: 0, Payload: payload}
		var buf bytes.Buffer
		if err := mux.WriteControlMessage(&buf, cm); err != nil {
			t.Fatalf("chunk %d exceeds the control frame limit: %v", msg.Sequence, err)
		}
		if buf.Len() > largest {
			largest = buf.Len()
		}
	}
	if largest >= 64<<10 {
		t.Fatalf("largest frame = %d bytes, want < 65536", largest)
	}
	t.Logf("largest control frame = %d bytes", largest)
}

func TestObjectStoreAssemblerRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"binary", []byte{0x00, 0xFF, 0x01, 0x80, 0x7F}},
		{"exact_chunk", bytes.Repeat([]byte{0x5A}, ObjectStoreChunkSize)},
		{"max", bytes.Repeat([]byte{0xC3}, sdk.MaxObjectSize)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msgs, err := NewObjectStoreDataChunks("req-"+c.name, ObjectStorePut, "com.example.store", "obj.bin", c.data)
			if err != nil {
				t.Fatalf("fragment: %v", err)
			}
			asm, err := NewObjectStoreAssembler(msgs[0])
			if err != nil {
				t.Fatalf("assemble first chunk: %v", err)
			}
			if err := asm.Add(msgs[0]); err != nil {
				t.Fatalf("add first chunk: %v", err)
			}
			for _, msg := range msgs[1:] {
				if err := asm.Add(msg); err != nil {
					t.Fatalf("add chunk %d: %v", msg.Sequence, err)
				}
			}
			if !asm.Complete() {
				t.Fatal("assembler is not complete")
			}
			if len(asm.Data()) != len(c.data) {
				t.Fatalf("assembled size = %d, want %d", len(asm.Data()), len(c.data))
			}
			if c.data != nil && !bytes.Equal(asm.Data(), c.data) {
				t.Fatal("assembled bytes differ from input")
			}
		})
	}
}

func TestObjectStoreAssemblerRejectsBadInput(t *testing.T) {
	data := bytes.Repeat([]byte{0x11}, ObjectStoreChunkSize*2+3)
	msgs, err := NewObjectStoreDataChunks("req-bad", ObjectStorePut, "com.example.store", "obj.bin", data)
	if err != nil {
		t.Fatalf("fragment: %v", err)
	}

	newAsm := func() *ObjectStoreAssembler {
		asm, err := NewObjectStoreAssembler(msgs[0])
		if err != nil {
			t.Fatalf("new assembler: %v", err)
		}
		return asm
	}

	t.Run("out_of_order", func(t *testing.T) {
		asm := newAsm()
		if err := asm.Add(msgs[0]); err != nil {
			t.Fatalf("add first: %v", err)
		}
		if err := asm.Add(msgs[2]); err == nil {
			t.Fatal("out-of-order chunk accepted")
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		asm := newAsm()
		if err := asm.Add(msgs[0]); err != nil {
			t.Fatalf("add first: %v", err)
		}
		if err := asm.Add(msgs[0]); err == nil {
			t.Fatal("duplicate chunk accepted")
		}
	})

	t.Run("metadata_mismatch", func(t *testing.T) {
		asm := newAsm()
		if err := asm.Add(msgs[0]); err != nil {
			t.Fatalf("add first: %v", err)
		}
		bad := *msgs[1]
		bad.ModuleID = "com.example.other"
		if err := asm.Add(&bad); err == nil {
			t.Fatal("chunk with another module ID accepted")
		}
	})

	t.Run("digest_mismatch", func(t *testing.T) {
		msgs, err := NewObjectStoreDataChunks("req-digest", ObjectStorePut, "com.example.store", "obj.bin", data)
		if err != nil {
			t.Fatalf("fragment: %v", err)
		}
		asm, err := NewObjectStoreAssembler(msgs[0])
		if err != nil {
			t.Fatalf("new assembler: %v", err)
		}
		if err := asm.Add(msgs[0]); err != nil {
			t.Fatalf("add first chunk: %v", err)
		}
		tampered := *msgs[1]
		tampered.Data = append([]byte(nil), msgs[1].Data...)
		tampered.Data[0] ^= 0xFF
		if err := asm.Add(&tampered); err != nil {
			t.Fatalf("add tampered chunk: %v", err)
		}
		var lastErr error
		for _, msg := range msgs[2:] {
			if lastErr = asm.Add(msg); lastErr != nil {
				break
			}
		}
		if lastErr == nil {
			t.Fatal("assembler accepted a corrupted object")
		}
		if asm.Complete() {
			t.Fatal("corrupted transfer reported complete")
		}
	})

	t.Run("oversized_first_chunk", func(t *testing.T) {
		big := &ObjectStoreMessage{
			RequestID:  "req-big",
			Op:         ObjectStorePut,
			ModuleID:   "com.example.store",
			Key:        "obj.bin",
			Sequence:   0,
			ChunkCount: 1,
			Final:      true,
			TotalSize:  sdk.MaxObjectSize + 1,
			SHA256:     ObjectStoreDigest(data),
			Data:       data,
		}
		if _, err := NewObjectStoreAssembler(big); err == nil {
			t.Fatal("oversized object metadata accepted")
		}
	})

	t.Run("chunk_length_mismatch", func(t *testing.T) {
		msgs, err := NewObjectStoreDataChunks("req-len", ObjectStorePut, "com.example.store", "obj.bin", data)
		if err != nil {
			t.Fatalf("fragment: %v", err)
		}
		asm, err := NewObjectStoreAssembler(msgs[0])
		if err != nil {
			t.Fatalf("new assembler: %v", err)
		}
		if err := asm.Add(msgs[0]); err != nil {
			t.Fatalf("add first: %v", err)
		}
		short := *msgs[1]
		short.Data = short.Data[:len(short.Data)-1]
		if err := asm.Add(&short); err == nil {
			t.Fatal("short chunk accepted")
		}
	})

	t.Run("invalid_key", func(t *testing.T) {
		bad := &ObjectStoreMessage{
			RequestID:  "req-key",
			Op:         ObjectStorePut,
			ModuleID:   "com.example.store",
			Key:        "../escape",
			Sequence:   0,
			ChunkCount: 1,
			Final:      true,
			TotalSize:  1,
			SHA256:     ObjectStoreDigest([]byte{0x00}),
			Data:       []byte{0x00},
		}
		if _, err := NewObjectStoreAssembler(bad); err == nil {
			t.Fatal("traversal key accepted")
		}
	})
}
