package agent

import (
	"bytes"
	"strings"
	"sync"
)

// logBuffer is a bounded, thread-safe ring buffer that captures recent
// log output line-by-line. The agent tees its standard log.Writer()
// into this buffer so the server can request recent log lines via the
// control channel (MsgLogRequest).
//
// Lines are split on newline boundaries; partial lines (no trailing \n)
// are accumulated and committed when the next Write arrives or the
// buffer fills.
type logBuffer struct {
	mu    sync.Mutex
	lines []string // circular storage
	head  int      // next write slot
	count int      // number of valid lines stored
	cap   int      // buffer capacity
	buf   bytes.Buffer // accumulates partial lines
}

// newLogBuffer creates a log buffer that retains up to cap lines.
func newLogBuffer(cap int) *logBuffer {
	if cap <= 0 {
		cap = 256
	}
	return &logBuffer{
		lines: make([]string, cap),
		cap:   cap,
	}
}

// Write implements io.Writer. It splits incoming data on newline
// boundaries and stores complete lines in the ring buffer. Partial
// lines (no trailing \n) are buffered until the next Write.
func (lb *logBuffer) Write(p []byte) (int, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	lb.buf.Write(p)
	for {
		line, err := lb.buf.ReadString('\n')
		if err != nil {
			// No complete line found; put the data back.
			lb.buf.WriteString(line)
			break
		}
		// Store the line without the trailing newline.
		lb.store(strings.TrimRight(line, "\r\n"))
	}
	return len(p), nil
}

// store adds a line to the ring buffer. Must be called with lb.mu held.
func (lb *logBuffer) store(line string) {
	lb.lines[lb.head] = line
	lb.head = (lb.head + 1) % lb.cap
	if lb.count < lb.cap {
		lb.count++
	}
}

// Read returns up to n recent log lines in chronological order
// (oldest first). Returns nil if the buffer is empty.
func (lb *logBuffer) Read(n int) []string {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	if lb.count == 0 || n <= 0 {
		return nil
	}
	if n > lb.count {
		n = lb.count
	}

	// Calculate the start position in the ring buffer.
	// head points to the NEXT write slot, so the oldest line is
	// at (head - count + cap) % cap when the buffer is full,
	// or 0 when it hasn't wrapped yet.
	start := (lb.head - lb.count + lb.cap) % lb.cap
	result := make([]string, 0, n)
	for i := lb.count - n; i < lb.count; i++ {
		idx := (start + i) % lb.cap
		result = append(result, lb.lines[idx])
	}
	return result
}
