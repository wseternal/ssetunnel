package server

import "sync"

// RingBuffer is a thread-safe, fixed-capacity byte buffer that overwrites
// the oldest data when full. Used to retain recent terminal output while
// a cloud shell client is disconnected, so reattaching clients can replay
// scrollback history.
type RingBuffer struct {
	mu   sync.Mutex
	buf  []byte
	size int // number of valid bytes currently stored
	head int // index of the oldest byte
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
	}
	return origN, nil
}

// ReadAll returns all buffered data in FIFO order and drains the buffer.
func (rb *RingBuffer) ReadAll() []byte {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.size == 0 {
		return nil
	}
	out := make([]byte, rb.size)
	if rb.head+rb.size <= len(rb.buf) {
		copy(out, rb.buf[rb.head:rb.head+rb.size])
	} else {
		first := len(rb.buf) - rb.head
		copy(out, rb.buf[rb.head:])
		copy(out[first:], rb.buf[:rb.size-first])
	}
	rb.head = 0
	rb.size = 0
	return out
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
