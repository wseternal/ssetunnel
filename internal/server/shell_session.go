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

	// mu protects sseWriter, flusher, and closed. The consumer goroutine
	// acquires this lock after each stream read to check whether a client
	// is attached and to write SSE frames. The Attach/Detach/Close methods
	// hold this lock while swapping the writer. This ensures that data
	// written to the ring buffer during the detach→attach gap is drained
	// atomically with the writer swap (no data loss or duplication).
	mu        sync.Mutex
	sseWriter io.Writer    // non-nil when a client is attached
	flusher   http.Flusher // flusher for the attached SSE writer
	closed    bool

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
		resizeCh:     make(chan windowSize, 1),
		metrics:      mc,
		ring:         NewRingBuffer(shellRingCap),
		wakeup:       make(chan struct{}, 1),
		done:         make(chan struct{}),
		createdAt:    now,
		lastActivity: now,
	}
}

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

// Attach connects a client to this session. It atomically drains the
// ring buffer as SSE frames and sets the SSE writer for live streaming.
// The caller must hold no locks and must block until the client
// disconnects, then call Detach.
//
// The drain and writer swap happen under the same lock as the consumer's
// post-read check, guaranteeing no data loss or duplication during the
// transition from detached to attached.
func (ss *ShellSession) Attach(w io.Writer, f http.Flusher) error {
	ss.mu.Lock()
	if ss.closed {
		ss.mu.Unlock()
		return fmt.Errorf("session closed")
	}
	if ss.sseWriter != nil {
		ss.mu.Unlock()
		return fmt.Errorf("already attached")
	}

	// Drain ring buffer as SSE frames while holding the lock. The
	// consumer cannot read from the stream and write to the buffer
	// concurrently because it also needs this lock to check the writer.
	data := ss.ring.ReadAll()
	if len(data) > 0 {
		if err := transport.WriteFrame(w, f, data); err != nil {
			ss.mu.Unlock()
			return fmt.Errorf("replay buffer: %w", err)
		}
	}

	// Set the live SSE writer. From now on, the consumer will write
	// SSE frames directly to w instead of buffering.
	ss.sseWriter = w
	ss.flusher = f
	ss.lastActivity = time.Now()
	ss.mu.Unlock()

	// Signal consumer to wake up (in case it was waiting).
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
	ss.lastActivity = time.Now()
	ss.mu.Unlock()
}

// Close permanently destroys the session: closes the yamux stream,
// pipes, and signals all goroutines to exit.
func (ss *ShellSession) Close() error {
	ss.mu.Lock()
	if ss.closed {
		ss.mu.Unlock()
		return nil
	}
	ss.closed = true
	ss.sseWriter = nil
	ss.flusher = nil
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
func (ss *ShellSession) IsDead() bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.closed
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
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.closed, ss.sseWriter != nil, ss.lastActivity
}

// outputConsumer reads from the yamux stream and either writes SSE
// frames to the attached client or buffers raw bytes into the ring
// buffer when detached. The lock is held for the entire
// read-check-write cycle to ensure atomicity with Attach/Detach.
func (ss *ShellSession) outputConsumer(heartbeat time.Duration) {
	defer ss.Close()

	buf := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(buf)

	for {
		// Read from yamux stream with heartbeat deadline.
		ss.stream.SetReadDeadline(time.Now().Add(heartbeat))
		n, err := ss.stream.Read(*buf)

		if n > 0 {
			// Snapshot writer state under lock, then release before
			// the potentially-blocking WriteFrame call so that
			// Detach/Close are not starved by TCP backpressure.
			ss.mu.Lock()
			closed := ss.closed
			w := ss.sseWriter
			f := ss.flusher
			data := make([]byte, n)
			copy(data, (*buf)[:n])
			ss.mu.Unlock()

			if closed {
				return
			}

			if w != nil {
				// Attached: write SSE frame outside the lock.
				if werr := transport.WriteFrame(w, f, data); werr != nil {
					// Client write failed — re-acquire lock to detach and buffer.
					ss.mu.Lock()
					ss.sseWriter = nil
					ss.flusher = nil
					ss.lastActivity = time.Now()
					ss.ring.Write(data)
					ss.mu.Unlock()
				} else {
					ss.metrics.RecordConnectBytes(ss.agentID, 0, n)
				}
			} else {
				// Detached: buffer raw bytes.
				ss.mu.Lock()
				ss.ring.Write(data)
				ss.mu.Unlock()
			}
		}

		if err == nil {
			continue
		}

		var nerr net.Error
		if errors.As(err, &nerr) && nerr.Timeout() {
			// Heartbeat timeout — send heartbeat to attached client.
			ss.mu.Lock()
			w := ss.sseWriter
			f := ss.flusher
			closed := ss.closed
			ss.mu.Unlock()

			if closed {
				return
			}

			if w != nil {
				if werr := transport.WriteHeartbeat(w, f); werr != nil {
					ss.Detach()
				}
			}

			// Wait for wakeup (new client attach) or next heartbeat.
			select {
			case <-ss.wakeup:
			case <-time.After(heartbeat):
			case <-ss.done:
				return
			}
			continue
		}

		// Stream closed (agent disconnect or error).
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
// NUL-prefixed JSON resize messages to the yamux stream. It exits when
// the session is closed.
func (ss *ShellSession) resizeForwarder() {
	for {
		select {
		case ws := <-ss.resizeCh:
			msg, _ := json.Marshal(ws)
			resizeMsg := append([]byte{0}, msg...)
			resizeMsg = append(resizeMsg, '\n')
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
		if ss.agentID == agentID && (userID < 0 || ss.userID == userID) && !ss.IsDead() {
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
			delete(r.sessions, id)
			continue
		}
		if !attached && now.Sub(lastAct) > timeout {
			log.Printf("shell: cleaning up idle session %s (agent=%s, idle=%v)", ss.id, ss.agentID, now.Sub(lastAct))
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

	for _, ss := range sessions {
		ss.Close()
	}
}
