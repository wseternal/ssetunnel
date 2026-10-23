package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/wseternal/ssetunnel/internal/metrics"
	"github.com/wseternal/ssetunnel/internal/transport"
)

// shellRingCap is the default ring buffer capacity for persistent shell
// sessions. 256 KiB retains roughly the last ~4000 lines of terminal
// output — enough for comfortable scrollback on reattach.
const shellRingCap = 256 << 10

// shellIdleTimeout is the default duration after which a detached shell
// session is considered stale and eligible for cleanup.
const shellIdleTimeout = 30 * time.Minute

// ShellSession is a persistent cloud shell session that outlives the
// HTTP SSE connection. It holds the yamux stream to the agent and a
// ring buffer for output captured while no client is attached.
//
// Lifecycle:
//  1. Created when a user first connects to a cloud shell
//  2. Client attaches: ring buffer drained as SSE, then live streaming resumes
//  3. Client disconnects: output continues to ring buffer only
//  4. Client reattaches: ring buffer replayed, then live streaming resumes
//  5. Session destroyed: explicit user action, agent disconnect, or idle timeout
type ShellSession struct {
	id      string
	agentID string
	userID  int64

	stream *yamux.Stream   // yamux stream to agent (shell PTY)
	upPipe *transport.Pipe // POST bodies → yamux stream (input)

	// writeMu serializes all writes to the yamux stream (from inputForwarder
	// and resizeForwarder). It is separate from mu to avoid holding the SSE
	// writer lock during blocking stream writes.
	writeMu  sync.Mutex
	resizeCh chan windowSize // PTY resize events drained by resizeForwarder
	metrics  *metrics.MetricsCollector // nil when metrics disabled

	// mu protects sseWriter, flusher, and rc. The consumer goroutine
	// acquires this lock after each stream read to check whether a client
	// is attached and to write SSE frames. The Attach/Detach/Close methods
	// hold this lock while swapping the writer. The closed flag is an
	// atomic.Bool so IsDead() can be read lock-free (avoids nested lock
	// acquisition in FindByUserAgent and CleanupIdle).
	mu        sync.Mutex
	sseWriter io.Writer              // non-nil when a client is attached
	flusher   http.Flusher           // flusher for the attached SSE writer
	rc        *http.ResponseController // per-conn write-deadline controller; nil when detached
	closed    atomic.Bool

	ring *RingBuffer // output ring buffer (always written to when detached)

	wakeup chan struct{} // signal consumer to check new writer / flush
	done   chan struct{} // closed when session is destroyed

	createdAt    time.Time
	lastActivity time.Time
}

// NewShellSession creates a persistent shell session wrapping the given
// yamux stream. The caller must call Start to begin the background goroutines.
func NewShellSession(id, agentID string, userID int64, stream *yamux.Stream, mc *metrics.MetricsCollector) *ShellSession {
	now := time.Now()
	return &ShellSession{
		id:           id,
		agentID:      agentID,
		userID:       userID,
		stream:       stream,
		upPipe:       transport.NewPipe(connectUpPipeCap),
		resizeCh:     make(chan windowSize, 16),
		metrics:      mc,
		ring:         NewRingBuffer(shellRingCap),
		wakeup:       make(chan struct{}, 1),
		done:         make(chan struct{}),
		createdAt:    now,
		lastActivity: now,
	}
}

// sseWriteDeadline bounds a single SSE frame write to an attached
// client. Refreshed per write; if the write does not complete within
// this window the client is treated as wedged and silently detached.
// 30 s matches the heartbeat cadence so a healthy client always beats
// the deadline, while a half-open TCP socket fails within one tick.
const sseWriteDeadline = 30 * time.Second

// ID returns the session identifier.
func (ss *ShellSession) ID() string { return ss.id }

// AgentID returns the agent identifier this session is connected to.
func (ss *ShellSession) AgentID() string { return ss.agentID }

// UserID returns the owning user's ID.
func (ss *ShellSession) UserID() int64 { return ss.userID }

// CreatedAt returns when the session was created.
func (ss *ShellSession) CreatedAt() time.Time { return ss.createdAt }

// BufferedBytes returns the number of bytes currently in the ring buffer.
func (ss *ShellSession) BufferedBytes() int { return ss.ring.Len() }

// Attached reports whether a client is currently attached to this session.
func (ss *ShellSession) Attached() bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.sseWriter != nil
}

// Done returns a channel that is closed when the session is destroyed.
func (ss *ShellSession) Done() <-chan struct{} { return ss.done }

// UpPipe returns the input pipe for this session. Connect-up handlers
// write user keystrokes here.
func (ss *ShellSession) UpPipe() *transport.Pipe { return ss.upPipe }

// Start launches the background goroutines: one reads from the yamux
// stream into the ring buffer (and SSE writer when attached), another
// reads from the up-pipe into the yamux stream (for user input).
func (ss *ShellSession) Start(heartbeat time.Duration) {
	go ss.outputConsumer(heartbeat)
	go ss.inputForwarder()
	go ss.resizeForwarder()
}

// Attach connects a client to this session. It atomically installs the
// SSE writer and drains the ring buffer as SSE frames under a single
// lock acquisition so no output bytes are skipped or duplicated across
// the attach transition, and no concurrent writes can race the replay.
// The caller must hold no locks and must block until the client
// disconnects, then call Detach.
//
// Byte ordering invariant: every byte that was in the ring reaches the
// HTTP response before any subsequent consumer byte. The consumer
// acquires ss.mu before writing any SSE frame, so holding the lock
// through the replay guarantees this ordering. To keep this invariant
// without deadlocking on a wedged browser, the replay applies a
// per-frame write deadline (sseWriteDeadline) — a wedged client
// detaches within one tick rather than blocking Attach indefinitely.
func (ss *ShellSession) Attach(w io.Writer, f http.Flusher) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.closed.Load() {
		return fmt.Errorf("session closed")
	}
	if ss.sseWriter != nil {
		return fmt.Errorf("already attached")
	}

	// Install the writer first, then drain the ring under the same lock.
	// Holding the lock through the replay serializes all writes to w:
	// the consumer cannot observe sseWriter until the replay is done,
	// so replay bytes always reach the wire before any live byte.
	ss.sseWriter = w
	ss.flusher = f
	// ResponseController requires http.ResponseWriter; the production
	// callers pass one, tests may pass a plain io.Writer.
	if rw, ok := w.(http.ResponseWriter); ok {
		ss.rc = http.NewResponseController(rw)
	}
	ss.lastActivity = time.Now()

	data := ss.ring.ReadAll()
	if len(data) > 0 {
		// Arm the per-frame write deadline so a wedged browser cannot
		// stall Attach indefinitely. The consumer applies the same
		// deadline on every subsequent frame write (F8).
		if rc := ss.rc; rc != nil {
			_ = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline))
		}
		if err := transport.WriteFrame(w, f, data); err != nil {
			// Replay failed — roll back the writer install so the
			// caller observes a clean state on return.
			ss.sseWriter = nil
			ss.flusher = nil
			ss.rc = nil
			return fmt.Errorf("replay buffer: %w", err)
		}
	}

	log.Printf("shell: attached client to session %s (agent=%s, replay=%d bytes)", ss.id, ss.agentID, len(data))

	// Signal consumer to wake up (in case it was waiting on a nil writer).
	// Non-blocking: the channel has a 1-slot buffer; a pending signal is
	// enough to nudge the consumer.
	select {
	case ss.wakeup <- struct{}{}:
	default:
	}
	return nil
}

// Detach disconnects the client from this session. The yamux stream
// stays alive and output continues buffering into the ring buffer.
func (ss *ShellSession) Detach() {
	ss.mu.Lock()
	ss.sseWriter = nil
	ss.flusher = nil
	ss.rc = nil
	ss.lastActivity = time.Now()
	ss.mu.Unlock()
	log.Printf("shell: detached session %s (agent=%s)", ss.id, ss.agentID)
}

// Close permanently destroys the session: closes the yamux stream,
// pipes, and signals all goroutines to exit.
func (ss *ShellSession) Close() error {
	if !ss.closed.CompareAndSwap(false, true) {
		return nil // already closed
	}

	ss.mu.Lock()
	ss.sseWriter = nil
	ss.flusher = nil
	ss.rc = nil
	ss.mu.Unlock()

	if ss.upPipe != nil {
		ss.upPipe.Close()
	}
	if ss.stream != nil {
		ss.stream.Close()
	}
	close(ss.done)
	return nil
}

// IsDead reports whether the session has been closed.
// Lock-free via atomic read so callers (e.g., FindByUserAgent) can check
// without acquiring ss.mu (avoids nested lock under registry lock).
func (ss *ShellSession) IsDead() bool {
	return ss.closed.Load()
}

// LastActivity returns the time of the last client activity.
func (ss *ShellSession) LastActivity() time.Time {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.lastActivity
}

// snapshotState returns the session's closed, attached, and lastActivity
// state under a single lock acquisition. Used by CleanupIdle to avoid
// three separate lock round-trips per session.
func (ss *ShellSession) snapshotState() (dead, attached bool, lastAct time.Time) {
	dead = ss.closed.Load() // atomic, no lock needed
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return dead, ss.sseWriter != nil, ss.lastActivity
}

// outputConsumer reads from the yamux stream and either writes SSE
// frames to the attached client or buffers raw bytes into the ring
// buffer when detached. Per-frame write deadlines bound the stall a
// wedged browser can inflict on the consumer, so Close/Detach are
// reachable within one heartbeat tick even under TCP backpressure.
//
// Stall-free loop: after sending a heartbeat, the consumer loops back
// to the stream read immediately rather than sleeping on the wakeup
// channel. The previous implementation slept an extra heartbeat tick
// after every idle read timeout, which added up to a 15 s echo stall
// on the first keystroke after any idle gap.
func (ss *ShellSession) outputConsumer(heartbeat time.Duration) {
	defer ss.Close()

	buf := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(buf)

	for {
		// Read from yamux stream with heartbeat deadline.
		ss.stream.SetReadDeadline(time.Now().Add(heartbeat))
		n, err := ss.stream.Read(*buf)

		if n > 0 {
			// Always write to ring buffer so reattaching clients can
			// replay scrollback history (not just the bytes captured
			// while detached).
			ss.ring.Write((*buf)[:n])

			// Snapshot writer state under lock, then release before
			// the potentially-blocking WriteFrame call so that
			// Detach/Close are not starved by TCP backpressure.
			ss.mu.Lock()
			ss.lastActivity = time.Now()
			closed := ss.closed.Load()
			w := ss.sseWriter
			f := ss.flusher
			rc := ss.rc
			ss.mu.Unlock()

			if closed {
				return
			}

			if w != nil {
				// Attached: refresh write deadline, then write the SSE
				// frame outside the lock using the pooled buffer.
				if rc != nil {
					_ = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline))
				}
				if werr := transport.WriteFrame(w, f, (*buf)[:n]); werr != nil {
					// Client write failed — re-acquire lock to detach.
					// Guard: only clear if the current writer is still the one
					// that failed (a new client may have attached in the meantime).
					ss.mu.Lock()
					if ss.sseWriter == w {
						ss.sseWriter = nil
						ss.flusher = nil
						ss.rc = nil
					}
					ss.mu.Unlock()
				}
				ss.metrics.RecordConnectBytes(ss.agentID, 0, n)
			} else {
				ss.metrics.RecordConnectBytes(ss.agentID, 0, n)
			}
		}

		if err == nil {
			continue
		}

		var nerr net.Error
		if errors.As(err, &nerr) && nerr.Timeout() {
			// Heartbeat timeout — send heartbeat to attached client,
			// then loop back to the read immediately (no sleep).
			ss.mu.Lock()
			w := ss.sseWriter
			f := ss.flusher
			rc := ss.rc
			closed := ss.closed.Load()
			ss.mu.Unlock()

			if closed {
				return
			}

			if w != nil {
				if rc != nil {
					_ = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline))
				}
				if werr := transport.WriteHeartbeat(w, f); werr != nil {
					// Guard: only detach if the current writer is still the one
					// that failed (a new client may have attached in the meantime).
					ss.mu.Lock()
					if ss.sseWriter == w {
						ss.sseWriter = nil
						ss.flusher = nil
						ss.rc = nil
						ss.lastActivity = time.Now()
					}
					ss.mu.Unlock()
				}
			}

			// Loop back to the read immediately — no post-heartbeat
			// sleep. The stream's read deadline provides the idle
			// cadence; adding a select here would delay the next
			// stream read by up to one heartbeat tick after every
			// idle gap, producing a visible echo stall.
			continue
		}

		// Stream closed (agent disconnect or error).
		if !errors.Is(err, io.EOF) {
			log.Printf("shell: stream closed (session=%s, agent=%s): %v", ss.id, ss.agentID, err)
		}
		return
	}
}

// inputForwarder reads from the up-pipe (user keystrokes from POST) and
// writes to the yamux stream (agent PTY).
func (ss *ShellSession) inputForwarder() {
	defer ss.Close()

	buf := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(buf)

	for {
		select {
		case <-ss.done:
			return
		default:
		}
		ss.upPipe.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := ss.upPipe.Read(*buf)
		if n > 0 {
			ss.writeMu.Lock()
			_, werr := ss.stream.Write((*buf)[:n])
			ss.writeMu.Unlock()
			if werr != nil {
				return // stream broken
			}
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue // timeout: retry
			}
			return // pipe closed
		}
	}
}

// resizeForwarder drains PTY resize events from resizeCh and writes
// length-prefixed JSON resize messages to the yamux stream. The wire
// format is: 0x00 | uint16be(len) | JSON payload. The 2-byte length
// bounds the agent-side read so a stray NUL byte (e.g. from
// Ctrl-Space) costs at most a bounded hiccup instead of swallowing
// keystrokes into an unbounded buffer until '\n'.
//
// Unlike the other two forwarders, this one historically died silently
// on the first write error without calling ss.Close(), leaving resize
// handling dead for the session's remaining life while resize POSTs
// kept returning 200. Now it calls ss.Close() on failure, matching
// inputForwarder and outputConsumer.
func (ss *ShellSession) resizeForwarder() {
	defer ss.Close()
	for {
		select {
		case ws := <-ss.resizeCh:
			msg, _ := json.Marshal(ws)
			resizeMsg := transport.EncodeResize(msg)
			if resizeMsg == nil {
				log.Printf("shell: resize message too large (%d bytes), dropping", len(msg))
				continue
			}
			ss.writeMu.Lock()
			_, err := ss.stream.Write(resizeMsg)
			ss.writeMu.Unlock()
			if err != nil {
				return
			}
		case <-ss.done:
			return
		}
	}
}

// ---------------------------------------------------------------------------
// ShellSessionRegistry
// ---------------------------------------------------------------------------

// ShellSessionRegistry tracks persistent shell sessions. It is separate
// from the agent session Registry and the ephemeral connectSessions map.
type ShellSessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]*ShellSession
}

// NewShellSessionRegistry creates an empty registry.
func NewShellSessionRegistry() *ShellSessionRegistry {
	return &ShellSessionRegistry{sessions: make(map[string]*ShellSession)}
}

// Store adds or replaces a session.
func (r *ShellSessionRegistry) Store(ss *ShellSession) {
	r.mu.Lock()
	r.sessions[ss.id] = ss
	r.mu.Unlock()
}

// Load returns the session for the given ID, or nil.
func (r *ShellSessionRegistry) Load(id string) *ShellSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[id]
}

// Delete removes a session by ID.
func (r *ShellSessionRegistry) Delete(id string) {
	r.mu.Lock()
	delete(r.sessions, id)
	r.mu.Unlock()
}

// FindByUserAgent returns the first live session matching the given
// user ID and agent ID. Admin callers (userID < 0) match any user.
func (r *ShellSessionRegistry) FindByUserAgent(userID int64, agentID string) *ShellSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ss := range r.sessions {
		if ss.agentID == agentID && (userID < 0 || ss.userID == userID) && !ss.closed.Load() {
			return ss
		}
	}
	return nil
}

// Range iterates over all sessions. The callback receives each session;
// return false to stop iteration.
func (r *ShellSessionRegistry) Range(fn func(*ShellSession) bool) {
	r.mu.Lock()
	sessions := make([]*ShellSession, 0, len(r.sessions))
	for _, ss := range r.sessions {
		sessions = append(sessions, ss)
	}
	r.mu.Unlock()

	for _, ss := range sessions {
		if !fn(ss) {
			break
		}
	}
}

// CleanupIdle closes and removes sessions that have been detached longer
// than the given idle timeout.
func (r *ShellSessionRegistry) CleanupIdle(timeout time.Duration) {
	now := time.Now()
	var toClose []*ShellSession

	r.mu.Lock()
	for id, ss := range r.sessions {
		dead, attached, lastAct := ss.snapshotState()
		if dead {
			log.Printf("shell: reaping dead session %s (agent=%s)", id, ss.agentID)
			delete(r.sessions, id)
			continue
		}
		if !attached && now.Sub(lastAct) > timeout {
			log.Printf("shell: cleaning up idle session %s (agent=%s, user=%d, idle=%v)", ss.id, ss.agentID, ss.userID, now.Sub(lastAct))
			toClose = append(toClose, ss)
			delete(r.sessions, id)
		}
	}
	r.mu.Unlock()

	for _, ss := range toClose {
		ss.Close()
	}
}

// StartCleanupLoop runs a background goroutine that periodically cleans
// up idle sessions. It stops when the done channel is closed.
func (r *ShellSessionRegistry) StartCleanupLoop(interval, timeout time.Duration, done <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				r.CleanupIdle(timeout)
			case <-done:
				return
			}
		}
	}()
}

// CloseAll closes every session in the registry.
func (r *ShellSessionRegistry) CloseAll() {
	r.mu.Lock()
	sessions := make([]*ShellSession, 0, len(r.sessions))
	for _, ss := range r.sessions {
		sessions = append(sessions, ss)
	}
	r.sessions = make(map[string]*ShellSession)
	r.mu.Unlock()

	log.Printf("shell: closing %d sessions on shutdown", len(sessions))
	for _, ss := range sessions {
		ss.Close()
	}
}
