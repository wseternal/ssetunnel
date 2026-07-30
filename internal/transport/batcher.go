package transport

import (
	"errors"
	"sync"
	"time"
)

// Default batching ceilings: 256 KiB amortizes one POST per batch under
// bulk load (VNC, file transfer) while staying interactive for small
// writes. The server advertises 1 MiB; caps negotiation clamps to
// min(want, advertised) when both sides speak cycle-2.
const (
	DefaultMaxBatchSize = 256 << 10 // 256 KiB
	DefaultMaxWait      = 25 * time.Millisecond
	// DefaultMaxQueuedBytes bounds buffered+queued bytes so a writer
	// faster than the serial-POST drain gets backpressure, not
	// unbounded growth (4 MiB ≈ one yamux window ×4).
	DefaultMaxQueuedBytes = 4 << 20
)

// ErrBatcherClosed is returned by Write after Close.
var ErrBatcherClosed = errors.New("batcher: write after close")

// Batcher accumulates Write calls into POST-sized frames. It flushes at
// maxSize, at maxWait under saturation, or immediately when the sender
// is idle — interactive traffic pays no batching delay (plan decision 2).
type Batcher struct {
	maxSize   int
	maxWait   time.Duration
	maxQueued int
	flush     func([]byte) error

	mu         sync.Mutex
	cond       sync.Cond // signals queue non-empty or closed
	space      sync.Cond // signals queued bytes below the cap or closed
	buf        []byte    // accumulation buffer
	queue      [][]byte  // complete batches awaiting the sender
	queued     int       // len(buf) + queued batch bytes
	busy       bool      // sender is inside flush, not waiting
	timer      *time.Timer
	timerArmed bool
	closed     bool
	flushErr   error // first flush error, sticky
	done       chan struct{}
}

// NewBatcher starts a sender goroutine that delivers batches to flush,
// one at a time and in order. Write blocks once maxQueued bytes are
// outstanding (maxQueued <= 0 → DefaultMaxQueuedBytes). Call Close to
// drain and stop it.
func NewBatcher(maxSize int, maxWait time.Duration, maxQueued int, flush func([]byte) error) *Batcher {
	if maxQueued <= 0 {
		maxQueued = DefaultMaxQueuedBytes
	}
	b := &Batcher{
		maxSize:   maxSize,
		maxWait:   maxWait,
		maxQueued: maxQueued,
		flush:     flush,
		done:      make(chan struct{}),
	}
	b.cond.L = &b.mu
	b.space.L = &b.mu
	go b.run()
	return b
}

// Write appends p to the buffer, blocking while the queue holds
// maxQueued bytes or more — backpressure for writers outrunning the
// serial POST drain. Close wakes blocked writers with ErrBatcherClosed.
func (b *Batcher) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for !b.closed && b.queued >= b.maxQueued {
		b.space.Wait()
	}
	if b.closed {
		return 0, ErrBatcherClosed
	}
	b.buf = append(b.buf, p...)
	b.queued += len(p)
	// Fragment at maxSize: no batch may exceed the wire ceiling (plan
	// decision 4 — a real proxy body cap kills oversized POSTs).
	for len(b.buf) >= b.maxSize {
		b.queue = append(b.queue, b.buf[:b.maxSize])
		b.buf = b.buf[b.maxSize:]
		b.cond.Signal()
	}
	if len(b.buf) == 0 {
		b.stopTimerLocked()
		return len(p), nil
	}
	switch {
	case !b.busy && len(b.queue) == 0:
		b.enqueueLocked() // sender idle: flush eagerly, no batching delay
	default:
		b.armTimerLocked() // sender busy: coalesce until size or maxWait
	}
	return len(p), nil
}

// Close flushes the remainder, waits for the sender to finish, and
// returns the first flush error, if any. It is idempotent.
func (b *Batcher) Close() error {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		if b.timerArmed {
			b.timer.Stop()
			b.timerArmed = false
		}
		if len(b.buf) > 0 {
			b.enqueueLocked()
		}
		b.cond.Signal()
		b.space.Broadcast() // wake writers blocked on a full queue
	}
	b.mu.Unlock()
	<-b.done
	return b.flushErr
}

// Err returns the first flush error, if any. It is sticky: once a POST
// fails the session is dead (plan decision 3).
func (b *Batcher) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.flushErr
}

// run is the single sender goroutine: the only caller of flush, which
// keeps batches ordered and flush calls serialized.
func (b *Batcher) run() {
	defer close(b.done)
	b.mu.Lock()
	for {
		if len(b.queue) == 0 {
			// Sender is idle: drain any buffered remainder eagerly
			// rather than waiting out the timer. While the queue is
			// non-empty the sender is busy and buf keeps accumulating.
			if len(b.buf) > 0 {
				b.enqueueLocked()
			}
			if len(b.queue) == 0 {
				if b.closed {
					b.mu.Unlock()
					return
				}
				b.busy = false
				b.cond.Wait()
				b.busy = true
			}
			continue
		}
		batch := b.queue[0]
		b.queue = b.queue[1:]
		b.queued -= len(batch)
		b.space.Broadcast() // space freed for blocked writers
		b.busy = true       // processing, even if we never blocked in Wait
		b.mu.Unlock()
		err := b.flush(batch)
		b.mu.Lock()
		if err != nil && b.flushErr == nil {
			b.flushErr = err
		}
	}
}

// enqueueLocked moves buf to the queue as one batch and wakes the
// sender. Flush is an atomic buffer swap under the mutex, so bytes can
// never be flushed twice or interleaved out of order.
func (b *Batcher) enqueueLocked() {
	b.queue = append(b.queue, b.buf)
	b.buf = nil
	b.stopTimerLocked()
	b.cond.Signal()
}

// stopTimerLocked disarms the coalescing timer, if armed.
func (b *Batcher) stopTimerLocked() {
	if b.timerArmed {
		b.timer.Stop()
		b.timerArmed = false
	}
}

// armTimerLocked starts the maxWait ceiling for coalescing. The timer
// is armed only while the sender is busy; idle flushes are eager.
func (b *Batcher) armTimerLocked() {
	if b.timerArmed || b.closed {
		return
	}
	b.timerArmed = true
	b.timer = time.AfterFunc(b.maxWait, b.onTimer)
}

// onTimer moves the accumulated buffer to the queue when maxWait
// elapses under saturation. It never emits an empty batch.
func (b *Batcher) onTimer() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.timerArmed = false
	if len(b.buf) > 0 {
		b.enqueueLocked()
	}
}

// SetMaxSize adjusts the batch size ceiling. Takes effect on the next
// Write call that fragments the buffer. Safe to call from any goroutine.
func (b *Batcher) SetMaxSize(n int) {
	b.mu.Lock()
	if n > 0 {
		b.maxSize = n
	}
	b.mu.Unlock()
}
