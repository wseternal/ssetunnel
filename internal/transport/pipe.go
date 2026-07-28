package transport

import (
	"io"
	"os"
	"sync"
	"time"
)

// timeoutError satisfies net.Error so callers see a real i/o timeout
// (plan decision 8: deadlines are implemented honestly, no silent no-ops).
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
func (timeoutError) Is(target error) bool {
	return target == os.ErrDeadlineExceeded
}

// Pipe is a bounded in-memory byte pipe whose Read and Write honor
// deadlines via mutex-guarded timer + select (plan decision 8). It
// exists because io.Pipe cannot express deadlines non-destructively.
type Pipe struct {
	mu        sync.Mutex
	buf       []byte
	cap       int
	closed    bool
	err       error // returned to readers once buf drains
	rDeadline time.Time
	wDeadline time.Time
	rSig      chan struct{} // cap 1: data arrived, closed, or deadline changed
	wSig      chan struct{} // cap 1: space freed, closed, or deadline changed
}

// NewPipe returns a Pipe that buffers at most capacity bytes before
// blocking writers.
func NewPipe(capacity int) *Pipe {
	if capacity <= 0 {
		capacity = 1
	}
	return &Pipe{
		cap:  capacity,
		rSig: make(chan struct{}, 1),
		wSig: make(chan struct{}, 1),
	}
}

// Read blocks until data is available, the pipe is closed, or the read
// deadline expires. Buffered data drains before the close error.
func (p *Pipe) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	for {
		p.mu.Lock()
		if len(p.buf) > 0 {
			n := copy(b, p.buf)
			p.buf = p.buf[n:]
			p.signalLocked(p.wSig)
			if len(p.buf) > 0 {
				p.signalLocked(p.rSig) // wake other readers
			}
			p.mu.Unlock()
			return n, nil
		}
		if p.closed {
			err := p.err
			p.mu.Unlock()
			return 0, err
		}
		dl := p.rDeadline
		p.mu.Unlock()
		if err := p.waitLocked(dl, p.rSig); err != nil {
			return 0, err
		}
	}
}

// Write appends b to the buffer, blocking while the pipe is full until
// space frees, the pipe closes, or the write deadline expires.
func (p *Pipe) Write(b []byte) (int, error) {
	written := 0
	for written < len(b) {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return written, io.ErrClosedPipe
		}
		if space := p.cap - len(p.buf); space > 0 {
			n := min(space, len(b)-written)
			p.buf = append(p.buf, b[written:written+n]...)
			written += n
			p.signalLocked(p.rSig)
			if written == len(b) {
				p.mu.Unlock()
				return written, nil
			}
		}
		dl := p.wDeadline
		p.mu.Unlock()
		if err := p.waitLocked(dl, p.wSig); err != nil {
			return written, err
		}
	}
	return written, nil
}

// waitLocked blocks on sig until kicked or the deadline expires.
func (p *Pipe) waitLocked(dl time.Time, sig <-chan struct{}) error {
	if dl.IsZero() {
		<-sig
		return nil
	}
	d := time.Until(dl)
	if d <= 0 {
		return timeoutError{}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-sig:
		return nil
	case <-t.C:
		return timeoutError{}
	}
}

// Close closes the pipe: readers get io.EOF after draining, writers get
// io.ErrClosedPipe.
func (p *Pipe) Close() error { return p.CloseWithError(io.EOF) }

// CloseWithError closes the pipe, delivering err to readers once the
// buffer drains. The first close wins; later calls are no-ops.
func (p *Pipe) CloseWithError(err error) error {
	if err == nil {
		err = io.EOF
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.err = err
	p.signalLocked(p.rSig)
	p.signalLocked(p.wSig)
	return nil
}

// SetReadDeadline sets the deadline for current and future Reads;
// waiters are kicked to re-evaluate.
func (p *Pipe) SetReadDeadline(t time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rDeadline = t
	p.signalLocked(p.rSig)
	return nil
}

// SetWriteDeadline sets the deadline for current and future Writes.
func (p *Pipe) SetWriteDeadline(t time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wDeadline = t
	p.signalLocked(p.wSig)
	return nil
}

// signalLocked kicks one waiter without blocking; the cap-1 channel
// coalesces repeated signals.
func (p *Pipe) signalLocked(sig chan struct{}) {
	select {
	case sig <- struct{}{}:
	default:
	}
}
