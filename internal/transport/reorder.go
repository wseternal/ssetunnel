package transport

import (
	"errors"
	"time"
)

// Reorder-window failure sentinels (cycle-2 plan decision 4: both mean
// fail fast — the server maps them to 409 = session death).
var (
	ErrWindowFull = errors.New("transport: reorder window full")
	ErrGapTimeout = errors.New("transport: reorder gap timeout")
)

// ReorderWindowSize bounds buffered out-of-order batches. Memory bound:
// 8 slots x 1 MiB batch ceiling = 8 MiB worst case per session.
const ReorderWindowSize = 8

// defaultGapTimeout fails an unhealed gap fast; yamux keepalive (30 s)
// guarantees upstream traffic within that span, so the piggybacked check
// on Push always gets a chance to fire.
const defaultGapTimeout = 25 * time.Second

// ReorderWindow reassembles seq-numbered batches into arrival-order
// delivery: in-order pushes pass through, out-of-order ones buffer until
// the gap heals. It is a pure core (plan decision 4): no goroutines, no
// timers — the gap timeout is checked piggyback on each Push. Not safe
// for concurrent use; the caller (Session.push) serializes.
type ReorderWindow struct {
	// GapTimeout bounds how long a missing base seq may stall the stream;
	// checked on each Push. 0 → defaultGapTimeout (25 s).
	GapTimeout time.Duration

	base  uint64            // next seq to deliver
	buf   map[uint64][]byte // out-of-order batches awaiting their base
	gapAt time.Time         // when the current gap opened (valid = gap)
	now   func() time.Time  // injectable clock for tests
}

// NewReorderWindow returns a window starting at seq 0.
func NewReorderWindow() *ReorderWindow {
	return &ReorderWindow{
		buf: make(map[uint64][]byte, ReorderWindowSize),
		now: time.Now,
	}
}

// Push feeds one batch. It returns every batch that became deliverable —
// the pushed one plus any buffered followers — in seq order; the caller
// must write them downstream in the returned order. A duplicate
// (seq < base) is dropped with (nil, nil). Window-full and gap-timeout
// are terminal for the session (409 semantics), so Push is not expected
// to be called again after a non-nil error.
func (w *ReorderWindow) Push(seq uint64, payload []byte) ([][]byte, error) {
	gapTimeout := w.GapTimeout
	if gapTimeout <= 0 {
		gapTimeout = defaultGapTimeout
	}
	// Piggybacked gap-timeout check: if the gap is unhealed (base still
	// missing and this push does not provide it) and has outlived
	// GapTimeout, fail fast.
	if !w.gapAt.IsZero() && seq != w.base {
		if _, ok := w.buf[w.base]; !ok && w.now().Sub(w.gapAt) > gapTimeout {
			return nil, ErrGapTimeout
		}
	}
	switch {
	case seq < w.base:
		return nil, nil // duplicate retry: ack and discard
	case seq == w.base:
		ready := [][]byte{payload}
		w.base++
		for len(w.buf) > 0 {
			p, ok := w.buf[w.base]
			if !ok {
				break
			}
			delete(w.buf, w.base)
			ready = append(ready, p)
			w.base++
		}
		if len(w.buf) == 0 {
			w.gapAt = time.Time{} // gap healed: reset the clock
		}
		return ready, nil
	default:
		if len(w.buf) >= ReorderWindowSize {
			return nil, ErrWindowFull
		}
		if len(w.buf) == 0 {
			w.gapAt = w.now() // first out-of-order batch opens the gap
		}
		w.buf[seq] = payload
		return nil, nil
	}
}
