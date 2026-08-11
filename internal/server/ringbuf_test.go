package server

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
)

func TestRingBuffer_BasicWriteRead(t *testing.T) {
	rb := NewRingBuffer(16)
	if rb.Len() != 0 {
		t.Fatalf("new buffer Len = %d, want 0", rb.Len())
	}
	if got := rb.ReadAll(); got != nil {
		t.Fatalf("empty ReadAll = %v, want nil", got)
	}

	data := []byte("hello")
	n, err := rb.Write(data)
	if err != nil || n != len(data) {
		t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(data))
	}
	if rb.Len() != 5 {
		t.Fatalf("Len = %d, want 5", rb.Len())
	}
	if got := rb.ReadAll(); !bytes.Equal(got, data) {
		t.Fatalf("ReadAll = %q, want %q", got, data)
	}
	if rb.Len() != 0 {
		t.Fatalf("after ReadAll Len = %d, want 0", rb.Len())
	}
}

func TestRingBuffer_WrapAround(t *testing.T) {
	rb := NewRingBuffer(8)

	// Write 6 bytes: [0,1,2,3,4,5,_,_]
	rb.Write([]byte("012345"))
	// ReadAll drains: head=0, size=0
	rb.ReadAll()

	// Write 5 bytes starting at head=0: [0,1,2,3,4,_,_,_]
	rb.Write([]byte("ABCDE"))
	// Now write 5 more: total 10, cap 8 → oldest 2 overwritten
	rb.Write([]byte("FGHIJ"))

	// Expected: last 8 bytes of "ABCDEFGHIJ" = "CDEFGHIJ"
	want := []byte("CDEFGHIJ")
	if got := rb.ReadAll(); !bytes.Equal(got, want) {
		t.Fatalf("ReadAll = %q, want %q", got, want)
	}
}

func TestRingBuffer_OverflowExactCapacity(t *testing.T) {
	rb := NewRingBuffer(4)

	// Write exactly capacity
	rb.Write([]byte("ABCD"))
	if got := rb.ReadAll(); !bytes.Equal(got, []byte("ABCD")) {
		t.Fatalf("ReadAll = %q, want ABCD", got)
	}

	// Write more than capacity: only last 4 bytes kept
	rb.Write([]byte("12345678"))
	if got := rb.ReadAll(); !bytes.Equal(got, []byte("5678")) {
		t.Fatalf("overflow ReadAll = %q, want 5678", got)
	}
}

func TestRingBuffer_MultipleWrapArounds(t *testing.T) {
	rb := NewRingBuffer(10)

	// Simulate multiple write cycles that wrap around several times.
	var allWritten []byte
	for i := 0; i < 20; i++ {
		chunk := []byte(fmt.Sprintf("%d", i))
		rb.Write(chunk)
		allWritten = append(allWritten, chunk...)
	}

	// The buffer should contain the last 10 bytes written.
	got := rb.ReadAll()
	want := allWritten[len(allWritten)-10:]
	if !bytes.Equal(got, want) {
		t.Fatalf("after many writes: ReadAll = %q, want %q", got, want)
	}
}

func TestRingBuffer_EmptyWrite(t *testing.T) {
	rb := NewRingBuffer(8)
	n, err := rb.Write(nil)
	if err != nil || n != 0 {
		t.Fatalf("Write(nil) = (%d, %v)", n, err)
	}
	n, err = rb.Write([]byte{})
	if err != nil || n != 0 {
		t.Fatalf("Write([]) = (%d, %v)", n, err)
	}
	if rb.Len() != 0 {
		t.Fatalf("Len = %d, want 0", rb.Len())
	}
}

func TestRingBuffer_Concurrent(t *testing.T) {
	rb := NewRingBuffer(1024)
	const writers = 4
	const reads = 100

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < reads; j++ {
				rb.Write([]byte("data"))
			}
		}()
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < reads; j++ {
				rb.ReadAll()
			}
		}()
	}
	wg.Wait()
	// No panics or data races = pass.
}

func TestRingBuffer_Cap(t *testing.T) {
	rb := NewRingBuffer(42)
	if rb.Cap() != 42 {
		t.Fatalf("Cap = %d, want 42", rb.Cap())
	}
}
