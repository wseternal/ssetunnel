package server

import (
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
	"github.com/wseternal/ssetunnel/internal/mux"
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

	stream   *yamux.Stream   // yamux data stream to agent (shell PTY)
	streamID uint32           // yamux stream ID for control stream routing
	agentSess *Session        // agent session (for SendControl); nil-safe
	upPipe   *transport.Pipe // POST bodies → yamux stream (input)

	// resizeCh carries PTY resize events from the HTTP handler to the
	// resizeForwarder, which sends them via the control stream.
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

	wakeup       chan struct{} // signal consumer to check new writer / flush
	done         chan struct{} // closed when session is destroyed
	consumerDone chan struct{} // closed when outputConsumer exits

	createdAt    time.Time
	lastActivity time.Time
}

// NewShellSession creates a persistent shell session wrapping the given
// yamux data stream. The caller must call Start to begin the background
// goroutines. agentSess is the agent's Session for sending control
// messages (resize); it may be nil in tests.
func NewShellSession(id, agentID string, userID int64, stream *yamux.Stream, agentSess *Session, mc *metrics.MetricsCollector) *ShellSession {
	now := time.Now()
	return &ShellSession{
		id:        id,
		agentID:   agentID,
		userID:    userID,
		stream:    stream,
		streamID:  stream.StreamID(),
		agentSess: agentSess,
		upPipe:    transport.NewPipe(connectUpPipeCap),
		resizeCh:  make(chan windowSize, 16),
		metrics:   mc,
		ring:      NewRingBuffer(shellRingCap),
		wakeup:    make(chan struct{}, 1),
		done:      make(chan struct{}),
		consumerDone: make(chan struct{}),
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

// replayChunkSize bounds each SSE frame emitted during Attach ring
// replay. Chunking amortizes lock pressure and lets a wedged client
// fail faster (one deadline tick per chunk instead of one for the
// entire 256 KiB ring).
const replayChunkSize = 64 << 10

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

// ConsumerDone returns a channel that is closed when the outputConsumer
// goroutine exits. Handlers wait on this after ss.Done() to ensure the
// consumer has finished writing the terminal closed frame before the
// handler returns (preventing concurrent writes to the same
// http.ResponseWriter).
func (ss *ShellSession) ConsumerDone() <-chan struct{} { return ss.consumerDone }

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
	// Replay the ring buffer in chunks. Each chunk gets its own write
	// deadline so a wedged browser fails within one tick per chunk
	// rather than stalling the entire replay. The lock is held for the
	// full replay to preserve the byte-ordering invariant: all buffered
	// bytes reach the HTTP response before any subsequent consumer byte.
	for off := 0; off < len(data); off += replayChunkSize {
		if ss.closed.Load() {
			ss.sseWriter = nil
			ss.flusher = nil
			ss.rc = nil
			return fmt.Errorf("session closed during replay")
		}
		end := off + replayChunkSize
		if end > len(data) {
			end = len(data)
		}
		if rc := ss.rc; rc != nil {
			if err := rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline)); err != nil {
				log.Printf("shell: set write deadline during replay failed (session=%s): %v", ss.id, err)
				ss.sseWriter = nil
				ss.flusher = nil
				ss.rc = nil
				return fmt.Errorf("replay set deadline: %w", err)
			}
		}
		if err := transport.WriteFrame(w, f, data[off:end]); err != nil {
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

// detachWriter clears the SSE writer if it still matches w, preventing
// a new client's writer from being cleared by a stale failure from the
// previous client. When touchActivity is true, lastActivity is updated
// (used on heartbeat failures to delay idle cleanup).
func (ss *ShellSession) detachWriter(w io.Writer, touchActivity bool) {
	ss.mu.Lock()
	if ss.sseWriter == w {
		ss.sseWriter = nil
		ss.flusher = nil
		ss.rc = nil
		if touchActivity {
			ss.lastActivity = time.Now()
		}
	}
	ss.mu.Unlock()
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
	defer close(ss.consumerDone)

	buf := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(buf)

	for {
		// Read from yamux stream with heartbeat deadline.
		ss.stream.SetReadDeadline(time.Now().Add(heartbeat))
		n, err := ss.stream.Read(*buf)

		if n > 0 {
			// Write to ring and snapshot SSE writer under a single lock
			// acquisition. This closes the race where the ring write
			// happened outside ss.mu: Attach could drain the ring between
			// the two operations, replay the bytes, and then the consumer
			// would write the same bytes again via WriteFrame (C1 fix).
			ss.mu.Lock()
			ss.ring.Write((*buf)[:n])
			ss.lastActivity = time.Now()
			closed := ss.closed.Load()
			w := ss.sseWriter
			f := ss.flusher
			var deadlineErr error
			if w != nil && ss.rc != nil {
				deadlineErr = ss.rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline))
				if deadlineErr != nil {
					log.Printf("shell: set write deadline failed (session=%s, agent=%s): %v", ss.id, ss.agentID, deadlineErr)
				}
			}
			ss.mu.Unlock()

			if closed {
				return
			}

			if w != nil {
				if deadlineErr != nil {
					ss.detachWriter(w, false)
				} else if werr := transport.WriteFrame(w, f, (*buf)[:n]); werr != nil {
					log.Printf("shell: detaching session %s (agent=%s, user=%d): write error: %v", ss.id, ss.agentID, ss.userID, werr)
					ss.detachWriter(w, false)
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
				var deadlineErr error
				if rc != nil {
					deadlineErr = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline))
					if deadlineErr != nil {
						log.Printf("shell: set write deadline for heartbeat failed (session=%s, agent=%s): %v", ss.id, ss.agentID, deadlineErr)
					}
				}
				if deadlineErr != nil {
					ss.detachWriter(w, true)
				} else if werr := transport.WriteHeartbeat(w, f); werr != nil {
					log.Printf("shell: detaching session %s (agent=%s, user=%d): heartbeat error: %v", ss.id, ss.agentID, ss.userID, werr)
					ss.detachWriter(w, true)
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
		// Write the terminal closed frame here (not in the handler) so
		// the sole writer to the http.ResponseWriter is always the
		// consumer. The handler waits on consumerDone after ss.Done()
		// unblocks, ensuring no concurrent writes.
		ss.mu.Lock()
		w := ss.sseWriter
		f := ss.flusher
		ss.mu.Unlock()
		if w != nil {
			if werr := transport.WriteClosedFrame(w, f); werr != nil {
				log.Printf("shell: write closed frame failed (session=%s): %v", ss.id, werr)
			}
		}
		return
	}
}

// inputForwarder reads from the up-pipe (user keystrokes from POST) and
// writes to the yamux data stream (agent PTY). This is the sole writer
// to the data stream — resize commands go through the control stream.
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
			_, werr := ss.stream.Write((*buf)[:n])
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

// resizeForwarder drains PTY resize events from resizeCh and sends them
// via the control stream to the agent. The agent's control dispatcher
// routes the resize message to the correct shell handler by stream ID.
// This replaces the old in-band NUL-byte length-prefixed protocol that
// multiplexed resize commands with PTY keystrokes on the data stream.
func (ss *ShellSession) resizeForwarder() {
	defer ss.Close()
	for {
		select {
		case ws := <-ss.resizeCh:
			msg := mux.NewResizeMessage(ss.streamID, ws.Cols, ws.Rows)
			if ss.agentSess == nil {
				log.Printf("shell: resize send failed (session=%s, no agent session)", ss.id)
				return
			}
			if err := ss.agentSess.SendControl(msg); err != nil {
				log.Printf("shell: resize send failed (session=%s, agent=%s): %v", ss.id, ss.agentID, err)
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
