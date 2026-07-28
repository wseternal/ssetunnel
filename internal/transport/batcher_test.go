package transport

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"
)

// batchCollector is a flush func sink for tests. If gate is non-nil the
// first flush blocks until it is closed, simulating a busy sender.
type batchCollector struct {
	gate chan struct{}

	mu      sync.Mutex
	batches [][]byte
}

func (c *batchCollector) flush(b []byte) error {
	// Only the batcher's single sender goroutine calls flush, so the
	// gate check needs no synchronization.
	if c.gate != nil {
		<-c.gate
		c.gate = nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batches = append(c.batches, append([]byte(nil), b...))
	return nil
}

func (c *batchCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.batches)
}

// snapshot returns a copy of the collected batches.
func (c *batchCollector) snapshot() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.batches...)
}

// waitBatches polls until n batches arrive or the (100x loose) deadline
// passes; tests are count-based, never tight-timing.
func (c *batchCollector) waitBatches(t *testing.T, n int) [][]byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.count() >= n {
			return c.snapshot()
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d batches, got %d", n, c.count())
	return nil
}

func concat(batches [][]byte) []byte {
	return bytes.Join(batches, nil)
}

func TestBatchEagerFlushWhenIdle(t *testing.T) {
	t.Parallel()
	c := &batchCollector{}
	b := NewBatcher(64, time.Hour, 0, c.flush) // hour: timer must not fire
	defer b.Close()

	if _, err := b.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	batches := c.waitBatches(t, 1)
	if got := string(batches[0]); got != "hello" {
		t.Fatalf("batch = %q, want %q", got, "hello")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Idle sender + no buffered data: exactly one batch, no empty flush.
	if n := c.count(); n != 1 {
		t.Fatalf("got %d batches, want exactly 1 (no empty flushes)", n)
	}
}

func TestBatchSizeFlushAtBoundary(t *testing.T) {
	t.Parallel()
	const maxSize = 64
	c := &batchCollector{gate: make(chan struct{})}
	b := NewBatcher(maxSize, time.Hour, 0, c.flush)

	// First write flushes eagerly and parks the sender on the gate.
	if _, err := b.Write([]byte{0x01}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Sender busy: 63 bytes must buffer, not flush.
	if _, err := b.Write(bytes.Repeat([]byte{0x02}, maxSize-1)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n := c.count(); n != 0 {
		t.Fatalf("got %d completed flushes while gated, want 0", n)
	}
	// Crossing exactly maxSize triggers the size flush.
	if _, err := b.Write([]byte{0x03}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	close(c.gate)
	batches := c.waitBatches(t, 2)
	if len(batches[1]) != maxSize {
		for i, bt := range batches {
			t.Logf("batch[%d] len=%d head=%v", i, len(bt), bt[:min(4, len(bt))])
		}
		t.Fatalf("size-flush batch len = %d, want exactly %d", len(batches[1]), maxSize)
	}
	want := append(bytes.Repeat([]byte{0x02}, maxSize-1), 0x03)
	if !bytes.Equal(batches[1], want) {
		t.Fatal("size-flush batch content mismatch")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := c.count(); n != 2 {
		t.Fatalf("got %d batches, want 2", n)
	}
}

func TestBatchCoalescingUnderSaturation(t *testing.T) {
	t.Parallel()
	const maxSize = 256
	c := &batchCollector{gate: make(chan struct{})}
	b := NewBatcher(maxSize, time.Hour, 0, c.flush) // timer off: only size/eager flush

	// Park the sender, then pile up writes that must coalesce.
	if _, err := b.Write([]byte("park")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var want []byte
	const writes = 20
	for i := 0; i < writes; i++ {
		chunk := bytes.Repeat([]byte{byte(i)}, 10)
		want = append(want, chunk...)
		if _, err := b.Write(chunk); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	close(c.gate)
	b.Close()
	batches := c.snapshot()
	// 20 writes of 10 B = 200 B < 256 B maxSize: one coalesced batch.
	if len(batches) != 2 {
		t.Fatalf("got %d batches, want 2 (parked write + 1 coalesced)", len(batches))
	}
	if got := concat(batches[1:]); !bytes.Equal(got, want) {
		t.Fatalf("coalesced bytes mismatch: got %d, want %d", len(got), len(want))
	}
}

func TestBatchTimerFlushWhenBusy(t *testing.T) {
	t.Parallel()
	c := &batchCollector{gate: make(chan struct{})}
	b := NewBatcher(64, 20*time.Millisecond, 0, c.flush) // 20ms timer, 2s assert = 100x

	// Park the sender so the second write waits on the timer.
	if _, err := b.Write([]byte("park")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := b.Write([]byte("timed")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	close(c.gate)
	batches := c.waitBatches(t, 2) // 2s loose bound, timer is 20ms
	if got := string(batches[1]); got != "timed" {
		t.Fatalf("timer batch = %q, want %q", got, "timed")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := c.count(); n != 2 {
		t.Fatalf("got %d batches, want 2", n)
	}
}

func TestBatchOrderPreservedSingleWriter(t *testing.T) {
	t.Parallel()
	c := &batchCollector{gate: make(chan struct{})}
	b := NewBatcher(64, time.Millisecond, 0, c.flush)

	// Saturate, then stream 10k ordered bytes through every flush path.
	if _, err := b.Write([]byte("park")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var want []byte
	for i := 0; i < 10000; i++ {
		want = append(want, byte(i))
		if _, err := b.Write([]byte{byte(i)}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	close(c.gate)
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := concat(c.snapshot())
	if !bytes.Equal(got, append([]byte("park"), want...)) {
		t.Fatal("reassembled stream does not equal input order")
	}
}

func TestBatchHammerRace(t *testing.T) {
	t.Parallel()
	c := &batchCollector{}
	b := NewBatcher(64, time.Millisecond, 0, c.flush)

	// 8 writers hammer around the 64 B boundary; every writer uses a
	// distinct fill byte so lost/duplicated bytes show up as bad counts.
	const writers = 8
	const writesEach = 200
	var wg sync.WaitGroup
	want := make(map[byte]int)
	for w := 0; w < writers; w++ {
		fill := byte('a' + w)
		chunkLen := (w%5 + 1) * 13 // 13..65: straddles maxSize
		want[fill] += writesEach * chunkLen
		wg.Add(1)
		go func() {
			defer wg.Done()
			chunk := bytes.Repeat([]byte{fill}, chunkLen)
			for i := 0; i < writesEach; i++ {
				if _, err := b.Write(chunk); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := make(map[byte]int)
	for _, batch := range c.snapshot() {
		if len(batch) == 0 {
			t.Fatal("empty batch emitted")
		}
		for _, by := range batch {
			got[by]++
		}
	}
	for fill, n := range want {
		if got[fill] != n {
			t.Errorf("byte %q: got %d, want %d (lost or duplicated bytes)", fill, got[fill], n)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got bytes for %d fills, want %d", len(got), len(want))
	}
}

func TestBatchCloseDrains(t *testing.T) {
	t.Parallel()
	c := &batchCollector{}
	b := NewBatcher(1<<20, time.Hour, 0, c.flush) // nothing forces a flush but Close

	var want []byte
	for i := 0; i < 100; i++ {
		chunk := bytes.Repeat([]byte{byte(i)}, 7)
		want = append(want, chunk...)
		if _, err := b.Write(chunk); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	batches := c.snapshot()
	if len(batches) == 0 {
		t.Fatal("Close did not drain buffered bytes")
	}
	if got := concat(batches); !bytes.Equal(got, want) {
		t.Fatalf("drained %d bytes, want %d", len(got), len(want))
	}
	// Writes after Close must fail, and Close must be idempotent.
	if _, err := b.Write([]byte("x")); err == nil {
		t.Fatal("Write after Close: expected error, got nil")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestBatchNoEmptyFlush(t *testing.T) {
	t.Parallel()
	c := &batchCollector{}
	b := NewBatcher(64, time.Millisecond, 0, c.flush)
	if n, err := b.Write(nil); n != 0 || err != nil {
		t.Fatalf("empty Write = (%d, %v), want (0, nil)", n, err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := c.count(); n != 0 {
		t.Fatalf("got %d batches with no data written, want 0", n)
	}
}

func TestBatchFlushError(t *testing.T) {
	t.Parallel()
	// Regression: a failing flush must not deadlock the sender (the
	// batcher used to re-lock its own mutex on the error path).
	flushErr := errors.New("post failed")
	calls := 0
	b := NewBatcher(64, time.Millisecond, 0, func([]byte) error {
		calls++
		return flushErr
	})
	if _, err := b.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := b.Close(); !errors.Is(err, flushErr) {
		t.Fatalf("Close = %v, want flush error %v", err, flushErr)
	}
	if got := b.Err(); !errors.Is(got, flushErr) {
		t.Fatalf("Err = %v, want sticky flush error", got)
	}
	if calls != 1 {
		t.Fatalf("flush called %d times, want 1", calls)
	}
}

func TestBatchFragmentsAtMaxSize(t *testing.T) {
	t.Parallel()
	c := &batchCollector{}
	b := NewBatcher(64, time.Hour, 0, c.flush)
	defer b.Close()
	// One yamux-window-sized write must not become one oversized POST:
	// fragment at maxSize (wire ceiling, plan decision 4).
	input := bytes.Repeat([]byte{'x'}, 200) // 3×64 + 8
	if _, err := b.Write(input); err != nil {
		t.Fatalf("Write: %v", err)
	}
	batches := c.waitBatches(t, 4)
	for i, want := range []int{64, 64, 64, 8} {
		if len(batches[i]) != want {
			t.Fatalf("batch[%d] len = %d, want %d", i, len(batches[i]), want)
		}
	}
	if got := concat(batches); !bytes.Equal(got, input) {
		t.Fatal("fragmented bytes do not reassemble to input")
	}
}

func TestBatchBackpressure(t *testing.T) {
	t.Parallel()
	c := &batchCollector{gate: make(chan struct{})}
	// Cap total queued bytes at 128 = two 64 B batches.
	b := NewBatcher(64, time.Hour, 128, c.flush)

	// First write flushes eagerly; the sender parks on the gate.
	if _, err := b.Write(bytes.Repeat([]byte{1}, 64)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Two more fill the queue to the cap.
	for i := 0; i < 2; i++ {
		if _, err := b.Write(bytes.Repeat([]byte{2}, 64)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	// Over the cap: Write must block, not grow the queue unboundedly.
	blocked := make(chan error, 1)
	go func() {
		_, err := b.Write(bytes.Repeat([]byte{3}, 64))
		blocked <- err
	}()
	select {
	case <-blocked:
		t.Fatal("Write returned despite the queue being at the cap")
	case <-time.After(100 * time.Millisecond):
	}
	// Unblock the sender: everything drains and the blocked Write lands.
	close(c.gate)
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatalf("blocked Write: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Write still blocked after the sender drained")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var total int
	for _, batch := range c.snapshot() {
		total += len(batch)
	}
	if total != 4*64 {
		t.Fatalf("flushed %d bytes, want %d", total, 4*64)
	}
}

func TestBatchBlockedWriteWakesOnClose(t *testing.T) {
	t.Parallel()
	c := &batchCollector{gate: make(chan struct{})}
	b := NewBatcher(64, time.Hour, 64, c.flush)
	if _, err := b.Write(bytes.Repeat([]byte{1}, 64)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := b.Write(bytes.Repeat([]byte{2}, 64)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	blocked := make(chan error, 1)
	go func() {
		_, err := b.Write(bytes.Repeat([]byte{3}, 64))
		blocked <- err
	}()
	time.Sleep(50 * time.Millisecond) // let it block on the full queue
	close(c.gate)                     // drain completes, then Close
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Whether the write landed or was refused, it must not hang.
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked Write hung across Close")
	}
}
