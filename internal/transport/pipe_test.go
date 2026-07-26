package transport

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestPipe_BasicReadWrite(t *testing.T) {
	p := NewPipe(64)
	data := []byte("hello world")
	n, err := p.Write(data)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != len(data) {
		t.Fatalf("write: got %d, want %d", n, len(data))
	}

	buf := make([]byte, 64)
	n, err = p.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != "hello world" {
		t.Fatalf("read: got %q, want %q", buf[:n], "hello world")
	}
}

func TestPipe_ZeroByteRead(t *testing.T) {
	p := NewPipe(64)
	n, err := p.Read(nil)
	if n != 0 || err != nil {
		t.Fatalf("zero-byte read: got (%d, %v), want (0, nil)", n, err)
	}
}

func TestPipe_ZeroByteWrite(t *testing.T) {
	p := NewPipe(64)
	n, err := p.Write(nil)
	if n != 0 || err != nil {
		t.Fatalf("zero-byte write: got (%d, %v), want (0, nil)", n, err)
	}
}

func TestPipe_CloseDeliversEOF(t *testing.T) {
	p := NewPipe(64)
	p.Close()

	buf := make([]byte, 1)
	_, err := p.Read(buf)
	if err != io.EOF {
		t.Fatalf("read after close: got %v, want io.EOF", err)
	}
}

func TestPipe_CloseWithError(t *testing.T) {
	p := NewPipe(64)
	customErr := errors.New("custom error")
	p.CloseWithError(customErr)

	buf := make([]byte, 1)
	_, err := p.Read(buf)
	if err != customErr {
		t.Fatalf("read after CloseWithError: got %v, want %v", err, customErr)
	}
}

func TestPipe_CloseWithErrorNil(t *testing.T) {
	p := NewPipe(64)
	p.CloseWithError(nil) // should default to io.EOF

	buf := make([]byte, 1)
	_, err := p.Read(buf)
	if err != io.EOF {
		t.Fatalf("CloseWithError(nil): got %v, want io.EOF", err)
	}
}

func TestPipe_WriteAfterClose(t *testing.T) {
	p := NewPipe(64)
	p.Close()

	_, err := p.Write([]byte("data"))
	if err != io.ErrClosedPipe {
		t.Fatalf("write after close: got %v, want io.ErrClosedPipe", err)
	}
}

func TestPipe_CapacityBlocking(t *testing.T) {
	p := NewPipe(4) // capacity 4

	// Fill the pipe.
	n, err := p.Write([]byte("abcd"))
	if err != nil || n != 4 {
		t.Fatalf("fill: got (%d, %v)", n, err)
	}

	// A 5th byte should block. Verify with a goroutine + timeout.
	done := make(chan struct{})
	go func() {
		p.Write([]byte("e")) //nolint:errcheck
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("write should have blocked on full pipe")
	case <-time.After(50 * time.Millisecond):
		// expected: blocked
	}

	// Drain one byte to unblock.
	buf := make([]byte, 1)
	p.Read(buf) //nolint:errcheck

	select {
	case <-done:
		// expected: unblocked
	case <-time.After(time.Second):
		t.Fatal("write should have unblocked after read")
	}
}

func TestPipe_DrainBeforeCloseError(t *testing.T) {
	p := NewPipe(64)

	// Write data then close with a custom error.
	p.Write([]byte("data")) //nolint:errcheck
	customErr := errors.New("connection reset")
	p.CloseWithError(customErr)

	// Should drain buffered data first.
	buf := make([]byte, 64)
	n, err := p.Read(buf)
	if err != nil || string(buf[:n]) != "data" {
		t.Fatalf("drain: got (%d, %v, %q), want (4, nil, data)", n, err, buf[:n])
	}

	// Then get the close error.
	_, err = p.Read(buf)
	if err != customErr {
		t.Fatalf("after drain: got %v, want %v", err, customErr)
	}
}

func TestPipe_ReadDeadline(t *testing.T) {
	p := NewPipe(64)
	p.SetReadDeadline(time.Now().Add(50 * time.Millisecond))

	buf := make([]byte, 1)
	_, err := p.Read(buf)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var nerr interface{ Timeout() bool }
	if !errors.As(err, &nerr) || !nerr.Timeout() {
		t.Fatalf("expected timeout error, got: %v", err)
	}
}

func TestPipe_WriteDeadline(t *testing.T) {
	p := NewPipe(1) // capacity 1
	p.Write([]byte("x")) //nolint:errcheck  // fill it

	p.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
	_, err := p.Write([]byte("y"))
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var nerr interface{ Timeout() bool }
	if !errors.As(err, &nerr) || !nerr.Timeout() {
		t.Fatalf("expected timeout error, got: %v", err)
	}
}

func TestPipe_DeadlineChangeWakesWaiter(t *testing.T) {
	p := NewPipe(64)

	// Set a far-future deadline, start a read, then change to past.
	p.SetReadDeadline(time.Now().Add(10 * time.Second))
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := p.Read(buf)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	p.SetReadDeadline(time.Now()) // change to now → should wake

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected timeout after deadline change")
		}
	case <-time.After(time.Second):
		t.Fatal("read did not wake after deadline change")
	}
}

func TestPipe_CloseWakesReadWaiter(t *testing.T) {
	p := NewPipe(64)
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := p.Read(buf)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	p.Close()

	select {
	case err := <-done:
		if err != io.EOF {
			t.Fatalf("expected io.EOF, got: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("read did not wake after close")
	}
}

func TestPipe_CloseWakesWriteWaiter(t *testing.T) {
	p := NewPipe(1)
	p.Write([]byte("x")) //nolint:errcheck // fill it

	done := make(chan error, 1)
	go func() {
		_, err := p.Write([]byte("y"))
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	p.Close()

	select {
	case err := <-done:
		if err != io.ErrClosedPipe {
			t.Fatalf("expected io.ErrClosedPipe, got: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("write did not wake after close")
	}
}

func TestPipe_DoubleCloseIsNoop(t *testing.T) {
	p := NewPipe(64)
	p.Close()
	p.Close() // should not panic

	buf := make([]byte, 1)
	_, err := p.Read(buf)
	if err != io.EOF {
		t.Fatalf("double close: got %v, want io.EOF", err)
	}
}

func TestPipe_ConcurrentReadWrite(t *testing.T) {
	p := NewPipe(1024)
	const numWrites = 100
	const bytesPerWrite = 64

	// Single writer goroutine.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		data := bytes.Repeat([]byte{'A'}, bytesPerWrite)
		for i := 0; i < numWrites; i++ {
			if _, err := p.Write(data); err != nil {
				return
			}
		}
		p.Close()
	}()

	// Single reader goroutine.
	totalRead := 0
	buf := make([]byte, 128)
	for {
		n, err := p.Read(buf)
		totalRead += n
		if err != nil {
			break
		}
	}

	wg.Wait()

	expected := numWrites * bytesPerWrite
	if totalRead != expected {
		t.Fatalf("concurrent read total: got %d, want %d", totalRead, expected)
	}
}

func TestPipe_PartialWrite(t *testing.T) {
	p := NewPipe(4) // capacity 4

	// Write 8 bytes — should block after 4 until space frees.
	done := make(chan int, 1)
	go func() {
		n, _ := p.Write([]byte("12345678"))
		done <- n
	}()

	time.Sleep(50 * time.Millisecond)
	// Read 4 bytes to unblock the writer.
	buf := make([]byte, 4)
	n, _ := p.Read(buf)
	if n != 4 || string(buf) != "1234" {
		t.Fatalf("first read: got %d %q", n, buf)
	}

	// Writer should complete.
	select {
	case n := <-done:
		if n != 8 {
			t.Fatalf("write: got %d, want 8", n)
		}
	case <-time.After(time.Second):
		t.Fatal("write did not complete after space freed")
	}

	// Read remaining 4 bytes.
	n, _ = p.Read(buf)
	if n != 4 || string(buf) != "5678" {
		t.Fatalf("second read: got %d %q", n, buf)
	}
}
