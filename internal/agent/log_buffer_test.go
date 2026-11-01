package agent

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestLogBuffer_BasicWriteRead(t *testing.T) {
	lb := newLogBuffer(4)
	if got := lb.Read(10); got != nil {
		t.Fatalf("empty Read = %v, want nil", got)
	}

	lb.Write([]byte("line1\nline2\nline3\n"))
	got := lb.Read(10)
	want := []string{"line1", "line2", "line3"}
	if len(got) != len(want) {
		t.Fatalf("Read = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Read[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLogBuffer_RingWrapAround(t *testing.T) {
	lb := newLogBuffer(3)
	// Write 5 lines into a 3-capacity buffer: oldest 2 are evicted.
	lb.Write([]byte("a\nb\nc\nd\ne\n"))
	got := lb.Read(10)
	want := []string{"c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("Read = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Read[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLogBuffer_PartialLineAccumulation(t *testing.T) {
	lb := newLogBuffer(4)
	// Write a partial line (no trailing newline).
	lb.Write([]byte("partial"))
	// Partial lines are not yet committed.
	if got := lb.Read(10); got != nil {
		t.Fatalf("Read before newline = %v, want nil", got)
	}
	// Complete the line.
	lb.Write([]byte(" line\n"))
	got := lb.Read(10)
	if len(got) != 1 || got[0] != "partial line" {
		t.Fatalf("Read = %v, want [\"partial line\"]", got)
	}
}

func TestLogBuffer_ReadLimitsCount(t *testing.T) {
	lb := newLogBuffer(10)
	lb.Write([]byte("a\nb\nc\nd\ne\n"))
	// Request only the last 2 lines.
	got := lb.Read(2)
	want := []string{"d", "e"}
	if len(got) != len(want) {
		t.Fatalf("Read(2) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Read(2)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLogBuffer_ReadZeroAndNegative(t *testing.T) {
	lb := newLogBuffer(4)
	lb.Write([]byte("a\nb\n"))
	if got := lb.Read(0); got != nil {
		t.Fatalf("Read(0) = %v, want nil", got)
	}
	if got := lb.Read(-1); got != nil {
		t.Fatalf("Read(-1) = %v, want nil", got)
	}
}

func TestLogBuffer_DefaultCapacity(t *testing.T) {
	lb := newLogBuffer(0)
	if lb.cap != 256 {
		t.Fatalf("default cap = %d, want 256", lb.cap)
	}
	lb = newLogBuffer(-5)
	if lb.cap != 256 {
		t.Fatalf("negative cap = %d, want 256", lb.cap)
	}
}

func TestLogBuffer_MultipleWrites(t *testing.T) {
	lb := newLogBuffer(10)
	// Simulate log output arriving in chunks.
	lb.Write([]byte("2026/01/01 starting up\n"))
	lb.Write([]byte("2026/01/01 connected to server\n"))
	lb.Write([]byte("2026/01/01 agent ready\n"))

	got := lb.Read(3)
	if len(got) != 3 {
		t.Fatalf("Read = %d lines, want 3", len(got))
	}
	if !strings.HasPrefix(got[0], "2026/01/01 starting") {
		t.Errorf("first line = %q", got[0])
	}
}

func TestLogBuffer_CarriageReturnStripped(t *testing.T) {
	lb := newLogBuffer(4)
	lb.Write([]byte("line1\r\nline2\r\n"))
	got := lb.Read(10)
	if len(got) != 2 {
		t.Fatalf("Read = %v, want 2 lines", got)
	}
	if got[0] != "line1" || got[1] != "line2" {
		t.Errorf("Read = %v, want [\"line1\", \"line2\"]", got)
	}
}

// TestLogBuffer_ConcurrentWriteRead exercises the mutex-guarded buffer
// with 2 writer goroutines and 1 reader goroutine running concurrently.
// The race detector catches any data races.
func TestLogBuffer_ConcurrentWriteRead(t *testing.T) {
	lb := newLogBuffer(256)
	const linesPerWriter = 100

	var wg sync.WaitGroup
	wg.Add(3)

	// Writer 1
	go func() {
		defer wg.Done()
		for i := 0; i < linesPerWriter; i++ {
			lb.Write([]byte(fmt.Sprintf("writer1-line%d\n", i)))
		}
	}()

	// Writer 2
	go func() {
		defer wg.Done()
		for i := 0; i < linesPerWriter; i++ {
			lb.Write([]byte(fmt.Sprintf("writer2-line%d\n", i)))
		}
	}()

	// Reader: periodically reads while writers are active.
	// Read() is a snapshot (returns all buffered lines), so we just
	// verify it doesn't panic or race.
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = lb.Read(10)
		}
	}()

	wg.Wait()

	// Final read: all 2*linesPerWriter lines should be in the buffer
	// (capacity 256 > 200 lines).
	all := lb.Read(1000)
	if len(all) != 2*linesPerWriter {
		t.Errorf("final read = %d lines, want %d", len(all), 2*linesPerWriter)
	}
}
