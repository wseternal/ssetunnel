package server

import "sync"

// RingBuffer is a thread-safe, fixed-capacity byte buffer that overwrites
// the oldest data when full. Used to retain recent terminal output while
// a cloud shell client is disconnected, so reattaching clients can replay
// scrollback history.
type RingBuffer struct {
	mu      sync.Mutex
	buf     []byte
	size    int  // number of valid bytes currently stored
	head    int  // index of the oldest byte
	evicted bool // oldest bytes were overwritten; head may sit mid-message
}

// NewRingBuffer creates a ring buffer with the given capacity in bytes.
func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{buf: make([]byte, capacity)}
}

// Write appends p to the buffer, overwriting the oldest data if necessary.
// It always returns len(p) and a nil error — writes never fail or block.
func (rb *RingBuffer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	origN := len(p)
	rb.mu.Lock()
	defer rb.mu.Unlock()

	cap := len(rb.buf)
	n := origN

	// If the write exceeds capacity, keep only the last cap bytes.
	if n > cap {
		p = p[n-cap:]
		n = cap
		// Everything older is gone and the retained head falls inside this
		// chunk, so it can begin at a partial message.
		rb.evicted = true
	}

	// Write position: (head + size) % cap
	writePos := (rb.head + rb.size) % cap

	// Copy into the circular buffer, handling wrap-around.
	if writePos+n <= cap {
		copy(rb.buf[writePos:], p)
	} else {
		first := cap - writePos
		copy(rb.buf[writePos:], p[:first])
		copy(rb.buf[0:], p[first:])
	}

	rb.size += n
	if rb.size > cap {
		// Overwrite happened: advance head past overwritten bytes.
		rb.head = (rb.head + rb.size - cap) % cap
		rb.size = cap
		rb.evicted = true
	}
	return origN, nil
}

// ReadAll returns all buffered data in FIFO order and drains the buffer.
func (rb *RingBuffer) ReadAll() []byte {
	data, _ := rb.Drained()
	return data
}

// Drained returns all buffered data in FIFO order, drains the buffer, and
// reports whether an eviction had overwritten part of the oldest retained
// write. Callers that re-send buffered data to a stateful consumer (a terminal,
// a protocol decoder) should use it: when evicted is true the returned slice
// may begin inside a message rather than at a write boundary.
func (rb *RingBuffer) Drained() ([]byte, bool) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.size == 0 {
		return nil, false
	}
	out := make([]byte, rb.size)
	if rb.head+rb.size <= len(rb.buf) {
		copy(out, rb.buf[rb.head:rb.head+rb.size])
	} else {
		first := len(rb.buf) - rb.head
		copy(out, rb.buf[rb.head:])
		copy(out[first:], rb.buf[:rb.size-first])
	}
	evicted := rb.evicted
	rb.head = 0
	rb.size = 0
	rb.evicted = false
	return out, evicted
}

// Len returns the number of bytes currently buffered.
func (rb *RingBuffer) Len() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.size
}

// Cap returns the buffer capacity.
func (rb *RingBuffer) Cap() int {
	return len(rb.buf)
}

// Evicted reports whether any data has been overwritten since the last
// Drained call. Unlike Drained, it does not reset the flag — callers
// use it to log the first overflow without draining the buffer.
func (rb *RingBuffer) Evicted() bool {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.evicted
}
