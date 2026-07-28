package transport

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"
)

// newTestWindow returns a window with a fixed, manually advanced clock so
// gap-timeout tests are count-based (no wall-clock assertions).
func newTestWindow() (*ReorderWindow, *time.Time) {
	now := time.Now()
	w := NewReorderWindow()
	w.now = func() time.Time { return now }
	return w, &now
}

func TestReorderInOrderPassthrough(t *testing.T) {
	t.Parallel()
	w, _ := newTestWindow()
	for i := 0; i < 8; i++ {
		want := []byte(fmt.Sprintf("batch-%d", i))
		ready, err := w.Push(uint64(i), want)
		if err != nil {
			t.Fatalf("Push(%d): %v", i, err)
		}
		if len(ready) != 1 || !bytes.Equal(ready[0], want) {
			t.Fatalf("Push(%d) ready = %q, want immediate %q", i, ready, want)
		}
	}
}

func TestReorderDuplicateDropped(t *testing.T) {
	t.Parallel()
	w, _ := newTestWindow()
	if _, err := w.Push(0, []byte("a")); err != nil {
		t.Fatalf("Push(0): %v", err)
	}
	ready, err := w.Push(0, []byte("a")) // seq < base: retry deduped
	if err != nil {
		t.Fatalf("duplicate Push(0): got %v, want nil", err)
	}
	if ready != nil {
		t.Fatalf("duplicate Push(0) ready = %q, want nil", ready)
	}
	// The window still expects seq 1.
	ready, err = w.Push(1, []byte("b"))
	if err != nil {
		t.Fatalf("Push(1): %v", err)
	}
	if len(ready) != 1 || string(ready[0]) != "b" {
		t.Fatalf("Push(1) ready = %q, want %q", ready, "b")
	}
}

func TestReorderWindowFull(t *testing.T) {
	t.Parallel()
	w, _ := newTestWindow()
	// Buffer seqs 1..16 (window size 16) without seq 0: the window is full.
	for i := uint64(1); i <= ReorderWindowSize; i++ {
		if _, err := w.Push(i, []byte{byte(i)}); err != nil {
			t.Fatalf("Push(%d): %v", i, err)
		}
	}
	if _, err := w.Push(ReorderWindowSize+1, []byte{byte(ReorderWindowSize + 1)}); !errors.Is(err, ErrWindowFull) {
		t.Fatalf("Push past window: got %v, want ErrWindowFull", err)
	}
	// The missing base still drains everything buffered so far.
	ready, err := w.Push(0, []byte{0})
	if err != nil {
		t.Fatalf("Push(0) heal: %v", err)
	}
	want := int(ReorderWindowSize) + 1
	if len(ready) != want {
		t.Fatalf("heal ready = %d batches, want %d", len(ready), want)
	}
	for i, b := range ready {
		if !bytes.Equal(b, []byte{byte(i)}) {
			t.Fatalf("heal batch %d = %v, want %v", i, b, []byte{byte(i)})
		}
	}
}

func TestReorderGapTimeout(t *testing.T) {
	t.Parallel()
	w, now := newTestWindow()
	w.GapTimeout = time.Second
	if _, err := w.Push(1, []byte("x")); err != nil {
		t.Fatalf("Push(1): %v", err)
	}
	// Inside the timeout: another out-of-order push is fine.
	*now = now.Add(w.GapTimeout - time.Millisecond)
	if _, err := w.Push(2, []byte("y")); err != nil {
		t.Fatalf("Push(2) inside gap timeout: %v", err)
	}
	// Past the timeout the unhealed gap fails fast.
	*now = now.Add(2 * time.Millisecond)
	if _, err := w.Push(3, []byte("z")); !errors.Is(err, ErrGapTimeout) {
		t.Fatalf("Push(3) past gap timeout: got %v, want ErrGapTimeout", err)
	}
}

func TestReorderGapHealedBeforeTimeout(t *testing.T) {
	t.Parallel()
	w, now := newTestWindow()
	w.GapTimeout = time.Second
	if _, err := w.Push(1, []byte("b")); err != nil {
		t.Fatalf("Push(1): %v", err)
	}
	// Any wait is fine when the gap heals first: the clock resets.
	*now = now.Add(10 * time.Second)
	ready, err := w.Push(0, []byte("a"))
	if err != nil {
		t.Fatalf("Push(0) heal: %v", err)
	}
	if len(ready) != 2 || string(ready[0]) != "a" || string(ready[1]) != "b" {
		t.Fatalf("heal ready = %q, want [a b]", ready)
	}
	// A fresh gap starts a fresh timeout window.
	*now = now.Add(10 * time.Second)
	if _, err := w.Push(3, []byte("d")); err != nil {
		t.Fatalf("Push(3) new gap: %v", err)
	}
	ready, err = w.Push(2, []byte("c"))
	if err != nil {
		t.Fatalf("Push(2) heal: %v", err)
	}
	if len(ready) != 2 || string(ready[0]) != "c" || string(ready[1]) != "d" {
		t.Fatalf("heal ready = %q, want [c d]", ready)
	}
}

// TestReorderAllPermutations pushes every one of the 8! = 40320 orderings
// of a full window and requires byte-exact reassembly in seq order.
func TestReorderAllPermutations(t *testing.T) {
	t.Parallel()
	const n = 8
	payloads := make([][]byte, n)
	for i := range payloads {
		payloads[i] = []byte(fmt.Sprintf("payload-%d-of-%d", i, n))
	}
	var want bytes.Buffer
	for _, p := range payloads {
		want.Write(p)
	}

	// Heap's algorithm: iterate all permutations without materializing them.
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	count := 0
	var run func(k int)
	run = func(k int) {
		if k == 1 {
			count++
			w, _ := newTestWindow()
			var got bytes.Buffer
			for _, seq := range idx {
				ready, err := w.Push(uint64(seq), payloads[seq])
				if err != nil {
					t.Fatalf("permutation %v: Push(%d): %v", idx, seq, err)
				}
				for _, b := range ready {
					got.Write(b)
				}
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Fatalf("permutation %v reassembled %q, want %q", idx, got.Bytes(), want.Bytes())
			}
			return
		}
		for i := 0; i < k; i++ {
			run(k - 1)
			if k%2 == 0 {
				idx[i], idx[k-1] = idx[k-1], idx[i]
			} else {
				idx[0], idx[k-1] = idx[k-1], idx[0]
			}
		}
	}
	run(n)
	if count != 40320 {
		t.Fatalf("iterated %d permutations, want 40320", count)
	}
}
