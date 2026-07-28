package server

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/wseternal/ssetunnel/internal/transport"
)

// Pipe capacities: big enough to absorb bench-scale bursts, small
// enough that backpressure reaches the peer quickly.
const (
	upPipeCap   = 256 << 10 // agent → server (POST bodies awaiting Read)
	downPipeCap = 256 << 10 // server → agent (Writes awaiting SSE send)
)

// Session is one tunnel session: the server side of an agent connection.
// It is a net.Conn whose Read yields upstream POST bytes and whose Write
// feeds the downstream SSE stream. Deadlines are honored by the
// underlying pipes (plan decision 8).
type Session struct {
	id   string
	up   *transport.Pipe // POST /up bodies → Read
	down *transport.Pipe // Write → GET /events stream

	// WriteTimeout bounds how long push may block on a full up pipe; a
	// POST must stay short-lived (spec boundary), so expiry kills the
	// session. Set before first use; 0 disables.
	WriteTimeout time.Duration

	// GapTimeout bounds how long the reorder window tolerates an unhealed
	// seq gap before push fails fast (409 = session death). Applies only
	// when the window is enabled; 0 → defaultGapTimeout.
	GapTimeout time.Duration

	mu      sync.Mutex
	nextSeq uint64                   // next upstream seq expected (plan decision 1)
	window  *transport.ReorderWindow // non-nil only when the agent negotiated concurrency>1

	createdAt     time.Time
	bytesSent     atomic.Uint64
	bytesReceived atomic.Uint64

	yamuxMu   sync.Mutex
	yamuxSess *yamux.Session // per-session yamux; set by attach after mux.Server

	closeOnce sync.Once
}

// defaultWriteTimeout keeps POST handlers short-lived when the consumer
// stalls (spec: every non-SSE request < 30 s).
const defaultWriteTimeout = 25 * time.Second

// defaultGapTimeout backstops a reassembly gap; yamux keepalive (30 s)
// guarantees upstream traffic within that span (cycle-2 plan decision 4).
const defaultGapTimeout = 25 * time.Second

// NewSession creates a session with the given agent-generated ID.
func NewSession(id string) *Session {
	return &Session{
		id:           id,
		up:           transport.NewPipe(upPipeCap),
		down:         transport.NewPipe(downPipeCap),
		WriteTimeout: defaultWriteTimeout,
		GapTimeout:   defaultGapTimeout,
		createdAt:    time.Now().UTC(),
	}
}

// Stats returns bytesSent, bytesReceived, and createdAt for the session.
func (s *Session) Stats() (uint64, uint64, time.Time) {
	return s.bytesSent.Load(), s.bytesReceived.Load(), s.createdAt
}

// enableWindow routes push through a reorder window (cycle-2 plan
// decision 3): called when the agent negotiated concurrency>1. Without
// it the legacy serial path runs verbatim.
func (s *Session) enableWindow() {
	s.window = transport.NewReorderWindow()
	s.window.GapTimeout = s.GapTimeout
}

// hasWindow reports whether the session negotiated concurrent upstream
// POSTs (and therefore accepts window semantics, gzip flags, etc.).
func (s *Session) hasWindow() bool { return s.window != nil }

// ID returns the agent-generated session ID.
func (s *Session) ID() string { return s.id }

// push accepts one upstream POST body with the given seq (plan decision
// 1: serial POSTs, monotonic seq). Old seqs are deduped (decision 3: a
// retry is idempotent-safe); a gap fails fast with 409 = session death.
func (s *Session) push(seq uint64, body []byte) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.window != nil {
		return s.pushWindowed(seq, body)
	}
	switch {
	case seq < s.nextSeq:
		return 200 // duplicate: ack and discard
	case seq > s.nextSeq:
		return 409 // gap: fail fast, session dies
	}
	if s.WriteTimeout > 0 {
		s.up.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
	}
	if _, err := s.up.Write(body); err != nil {
		// Full pipe + write timeout (or closed session): fail fast, the
		// session is dead either way (plan decision 3).
		s.Close()
		return 409
	}
	s.nextSeq++
	return 200
}

// pushWindowed is the negotiated-concurrency push path (cycle-2 plan
// decisions 3+4): the reorder window buffers out-of-order batches, and
// each batch that becomes deliverable feeds the up pipe in seq order.
// Window errors and pipe-write failures share the legacy semantics:
// 409 = session death.
func (s *Session) pushWindowed(seq uint64, body []byte) int {
	ready, err := s.window.Push(seq, body)
	if err != nil {
		s.Close()
		return 409
	}
	for _, batch := range ready {
		if s.WriteTimeout > 0 {
			s.up.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
		}
		if _, err := s.up.Write(batch); err != nil {
			s.Close()
			return 409
		}
	}
	return 200
}

// Read returns upstream bytes in POST order.
func (s *Session) Read(b []byte) (int, error) { return s.up.Read(b) }

// Write queues downstream bytes for the SSE stream.
func (s *Session) Write(b []byte) (int, error) { return s.down.Write(b) }

// Close tears down the session: pipes are closed first, which causes
// the SSE handler to return and the HTTP response to close — the agent
// detects this and reconnects. The yamux session shuts itself down via
// its recv goroutine which detects the pipe closure; we must NOT call
// ms.Close() here because yamux's Close() calls s.conn.Close() (this
// Session), causing a deadlock between closeOnce and yamux's shutdownLock.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.up.Close()
		s.down.Close()
	})
	return nil
}

// SetYamuxSession attaches a yamux session to this tunnel session.
func (s *Session) SetYamuxSession(ms *yamux.Session) {
	s.yamuxMu.Lock()
	s.yamuxSess = ms
	s.yamuxMu.Unlock()
}

// YamuxSession returns the attached yamux session, or nil.
func (s *Session) YamuxSession() *yamux.Session {
	s.yamuxMu.Lock()
	ms := s.yamuxSess
	s.yamuxMu.Unlock()
	return ms
}

// tunnelAddr is a placeholder net.Addr: a tunnel conn has no meaningful
// network address of its own.
type tunnelAddr string

func (a tunnelAddr) Network() string { return "ssetunnel" }
func (a tunnelAddr) String() string  { return string(a) }

// LocalAddr reports the session endpoint on the server.
func (s *Session) LocalAddr() net.Addr { return tunnelAddr("session/" + s.id) }

// RemoteAddr reports the session endpoint on the agent side.
func (s *Session) RemoteAddr() net.Addr { return tunnelAddr("agent/" + s.id) }

// SetDeadline sets read and write deadlines.
func (s *Session) SetDeadline(t time.Time) error {
	if err := s.SetReadDeadline(t); err != nil {
		return err
	}
	return s.SetWriteDeadline(t)
}

// SetReadDeadline sets the upstream (Read) deadline.
func (s *Session) SetReadDeadline(t time.Time) error { return s.up.SetReadDeadline(t) }

// SetWriteDeadline sets the downstream (Write) deadline.
func (s *Session) SetWriteDeadline(t time.Time) error { return s.down.SetWriteDeadline(t) }

var _ net.Conn = (*Session)(nil)

// Registry tracks live sessions by ID. Constructed and injected, never
// package-level (spec anti-pattern, plan decision 5).
type Registry struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewRegistry returns an empty session registry.
func NewRegistry() *Registry {
	return &Registry{sessions: make(map[string]*Session)}
}

// Get returns the session for id, or nil.
func (r *Registry) Get(id string) *Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[id]
}

// Replace registers s under its ID, closing any stale session with the
// same ID (plan decision 5: a reconnecting agent's new ID replaces the
// stale session).
func (r *Registry) Replace(s *Session) {
	r.mu.Lock()
	old := r.sessions[s.id]
	r.sessions[s.id] = s
	r.mu.Unlock()
	if old != nil && old != s {
		old.Close()
	}
}

// Remove deletes id only if it still maps to s, so a dead session's
// cleanup cannot evict its live replacement.
func (r *Registry) Remove(id string, s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[id] == s {
		delete(r.sessions, id)
	}
}

// Range iterates over all live sessions in the registry until fn returns false.
func (r *Registry) Range(fn func(*Session) bool) {
	r.mu.Lock()
	sessions := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()

	for _, s := range sessions {
		if !fn(s) {
			break
		}
	}
}

// IDs returns the IDs of all registered sessions.
func (r *Registry) IDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.sessions))
	for id := range r.sessions {
		ids = append(ids, id)
	}
	return ids
}
