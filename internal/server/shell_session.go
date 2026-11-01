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

	stream    *yamux.Stream   // yamux data stream to agent (shell PTY)
	streamID  uint32          // yamux stream ID for control stream routing
	agentSess *Session        // agent session (for SendControl); nil-safe
	upPipe    *transport.Pipe // POST bodies → yamux stream (input)

	// resizeCh carries PTY resize events from the HTTP handler to the
	// resizeForwarder, which sends them via the control stream.
	resizeCh chan windowSize           // PTY resize events drained by resizeForwarder
	metrics  *metrics.MetricsCollector // nil when metrics disabled

	// mu protects sseWriter, flusher, and rc. The consumer goroutine
	// acquires this lock after each stream read to check whether a client
	// is attached and to write SSE frames. The Attach/detachWriter/Close methods
	// hold this lock while swapping the writer. The closed flag is an
	// atomic.Bool so IsDead() can be read lock-free (avoids nested lock
	// acquisition in FindByUserAgent and CleanupIdle).
	mu        sync.Mutex
	sseWriter io.Writer                // non-nil when a client is attached
	flusher   http.Flusher             // flusher for the attached SSE writer
	rc        *http.ResponseController // per-conn write-deadline controller; nil when detached
	closed    atomic.Bool

	// frameMu serializes SSE frame writes (data, heartbeat, closed) and
	// lets a takeover or handler exit drain an in-flight write to the
	// previous writer before returning the response.
	frameMu sync.Mutex

	// clientLost is created per successful Attach and closed by
	// detachWriter when the attached client's SSE writer fails. The
	// handler selects on it and returns, terminating the HTTP response
	// so the browser reconnects. A fresh channel per attach prevents a
	// stale closed channel from ending a later client's response.
	clientLost chan struct{}

	ring *RingBuffer // output ring buffer (always written to when detached)

	done         chan struct{} // closed when session is destroyed
	consumerDone chan struct{} // closed when outputConsumer exits

	createdAt    time.Time
	lastActivity time.Time
}

// ShellSessionConfig carries the parameters for creating a new shell
// session. Using a config struct keeps NewShellSession readable as the
// set of dependencies grows.
type ShellSessionConfig struct {
	ID        string                    // connect session ID (client-generated)
	AgentID   string                    // target agent identifier
	UserID    int64                     // owning user's ID
	Stream    *yamux.Stream             // yamux data stream to agent (shell PTY)
	AgentSess *Session                  // agent session for SendControl; nil in tests
	Metrics   *metrics.MetricsCollector // nil when metrics disabled
}

// NewShellSession creates a persistent shell session wrapping the given
// yamux data stream. The caller must call Start to begin the background
// goroutines.
func NewShellSession(cfg ShellSessionConfig) *ShellSession {
	now := time.Now()
	return &ShellSession{
		id:           cfg.ID,
		agentID:      cfg.AgentID,
		userID:       cfg.UserID,
		stream:       cfg.Stream,
		streamID:     cfg.Stream.StreamID(),
		agentSess:    cfg.AgentSess,
		upPipe:       transport.NewPipe(connectUpPipeCap),
		resizeCh:     make(chan windowSize, 16),
		metrics:      cfg.Metrics,
		ring:         NewRingBuffer(shellRingCap),
		done:         make(chan struct{}),
		consumerDone: make(chan struct{}),
		createdAt:    now,
		lastActivity: now,
	}
}

// sseWriteDeadline bounds a single SSE frame write to an attached
// client. Refreshed per write; if the write does not complete within
// this window the client is detached and its handler ends the response
// so the browser reconnects (see clientLost).
// 30 s matches the heartbeat cadence so a healthy client always beats
// the deadline, while a half-open TCP socket fails within one tick.
const sseWriteDeadline = 30 * time.Second

// deadlineError normalises an http.ResponseController.SetWriteDeadline failure.
// http.ErrNotSupported means the attached ResponseWriter cannot express
// deadlines at all — a wrapped writer, a proxy shim, or a test recorder — which
// says nothing about whether the client is alive. Treating it as a dead writer
// would detach every healthy client that happens to lack deadline support, so
// it collapses to nil and the frame is written unguarded. Any other error is a
// real failure and is returned unchanged, keeping the detach-and-reconnect path.
//
// Trade-off: an unguarded write to a client that stops reading can park the
// consumer inside transport.WriteFrame while it holds frameMu, and Attach holds
// ss.mu across the replay. net/http's own write path supports deadlines, so no
// production writer reaches this branch today; it exists for wrapped writers and
// for tests. If a reverse proxy ever hands the shell a deadline-incapable
// ResponseWriter, bound the stall there rather than reverting this.
func deadlineError(err error) error {
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

// replayChunkSize bounds each SSE frame emitted during Attach ring
// replay. Chunking amortizes lock pressure and lets a wedged client
// fail faster (one deadline tick per chunk instead of one for the
// entire 256 KiB ring).
const replayChunkSize = 64 << 10

// safeReplayStart returns the offset into data where a terminal replay can begin
// without rendering junk, and is called ONLY when the ring evicted older bytes
// (see Attach), so a replay of a never-overflowed buffer is never trimmed.
//
// An eviction advances the head by an arbitrary byte count with no notion of
// message boundaries, so the retained prefix can be the tail of an ANSI sequence
// whose introducer was overwritten — "1;31mABC" is what survives of
// "\x1b[1;31mABC". A terminal shows those bytes literally and the garbage then
// persists in the scrollback.
//
// Best effort, not a VT parser. It skips a leading run of CSI parameter bytes
// (0x30-0x3F: digits, ':', ';', '<', '=', '>', '?') that is terminated by a CSI
// final byte (0x40-0x7E), and a leading BEL (the terminator of an evicted
// OSC/DCS). Both are strong truncation signals once an eviction is known to have
// happened. Known limits, accepted deliberately because a wider rule would drop
// real output:
//   - the intermediate range 0x20-0x2F is excluded: a leading space is ordinary
//     indented output, which is far more common than a truncated intermediate;
//   - a tail that begins with a non-parameter byte (a bare final byte, "[" from a
//     one-byte eviction, or continuation bytes of split UTF-8) is passed through
//     and still renders as junk;
//   - evicted output that happens to start with parameters followed by a letter
//     ("512M") is treated as a tail and skipped.
//
// Whatever is skipped is unrecoverable: the buffer has already been drained.
func safeReplayStart(data []byte) int {
	off := 0
	for off < len(data) && data[off] == 0x07 {
		off++
	}
	if off >= len(data) {
		return off
	}
	if b := data[off]; b >= 0x30 && b <= 0x3F {
		end := off
		for end < len(data) && data[end] >= 0x30 && data[end] <= 0x3F {
			end++
		}
		if end < len(data) && data[end] >= 0x40 && data[end] <= 0x7E {
			return end + 1
		}
	}
	return off
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
// disconnects, then call detachWriter.
//
// The returned channel is closed if the attached client's SSE writer
// fails (write error or deadline). The handler must select on it and
// return so the HTTP response terminates and the browser reconnects;
// the session itself stays alive for reattach.
//
// takeover=true replaces an existing attachment: the previous client's
// loss channel is closed and its handler ends its response. The stalled
// client that triggered the frontend watchdog sets takeover explicitly;
// a plain second client still gets "already attached" — serveShellSSE
// turns that into a 409 before flushing SSE headers — preventing two
// healthy viewers from evicting each other in a reconnect loop.
//
// Byte ordering invariant: every byte that was in the ring reaches the
// HTTP response before any subsequent consumer byte. The consumer
// acquires ss.mu before writing any SSE frame, so holding the lock
// through the replay guarantees this ordering. To keep this invariant
// without deadlocking on a wedged browser, the replay applies a
// per-frame write deadline (sseWriteDeadline) — a wedged client
// detaches within one tick rather than blocking Attach indefinitely.
func (ss *ShellSession) Attach(w io.Writer, f http.Flusher, takeover bool) (<-chan struct{}, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.closed.Load() {
		return nil, fmt.Errorf("session closed")
	}
	if ss.sseWriter != nil {
		if !takeover {
			return nil, fmt.Errorf("already attached")
		}
		// Drain any in-flight frame write to the previous writer before
		// signalling its handler, so the response cannot be closed while
		// the consumer is still writing to it. Safe under ss.mu: the
		// consumer releases frameMu before it next acquires ss.mu.
		ss.drainFrameWrite()
		ss.clearWriterLocked(true)
		log.Printf("shell: client takeover for session %s (agent=%s, user=%d)", ss.id, ss.agentID, ss.userID)
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

	data, evicted := ss.ring.Drained()
	// Only an eviction can leave the retained head inside a message; a ring
	// that never overflowed starts at a real write boundary and must be replayed
	// untouched, so genuine output is never trimmed.
	if evicted {
		if start := safeReplayStart(data); start > 0 {
			log.Printf("shell: dropped %d truncated bytes from replay (session=%s, agent=%s, ring_cap=%d)", start, ss.id, ss.agentID, ss.ring.Cap())
			data = data[start:]
		}
	}
	// Replay the ring buffer in chunks. Each chunk gets its own write
	// deadline so a wedged browser fails within one tick per chunk
	// rather than stalling the entire replay. The lock is held for the
	// full replay to preserve the byte-ordering invariant: all buffered
	// bytes reach the HTTP response before any subsequent consumer byte.
	//
	// On mid-replay failure (write error, deadline, session close), the
	// unsent tail is re-written to the ring so it survives for the next
	// reattach rather than being silently lost.
	sent := 0
	for off := 0; off < len(data); off += replayChunkSize {
		if ss.closed.Load() {
			ss.ring.Write(data[off:])
			ss.clearWriterLocked(false)
			return nil, fmt.Errorf("session closed during replay")
		}
		end := off + replayChunkSize
		if end > len(data) {
			end = len(data)
		}
		if rc := ss.rc; rc != nil {
			if err := deadlineError(rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline))); err != nil {
				log.Printf("shell: set write deadline during replay failed (session=%s): %v", ss.id, err)
				ss.ring.Write(data[off:])
				ss.clearWriterLocked(false)
				return nil, fmt.Errorf("replay set deadline: %w", err)
			}
		}
		if err := transport.WriteFrame(w, f, data[off:end]); err != nil {
			ss.ring.Write(data[off:])
			ss.clearWriterLocked(false)
			return nil, fmt.Errorf("replay buffer: %w", err)
		}
		sent = end
	}

	lost := make(chan struct{})
	ss.clientLost = lost

	log.Printf("shell: attached client to session %s (agent=%s, replay=%d/%d bytes)", ss.id, ss.agentID, sent, len(data))

	return lost, nil
}

// drainFrameWrite waits for any in-flight SSE frame write to finish.
// It may be called while holding ss.mu: the consumer releases frameMu
// before it next acquires ss.mu, so the wait cannot deadlock.
func (ss *ShellSession) drainFrameWrite() {
	ss.frameMu.Lock()
	ss.frameMu.Unlock()
}

// clearWriterLocked clears the attached client's writer state. The
// per-attach clientLost channel is closed only when signalLost is true
// (writer-failure and takeover paths); normal detach and session close
// must not signal loss because their handlers are already returning or
// the Done path handles cleanup.
func (ss *ShellSession) clearWriterLocked(signalLost bool) {
	ss.sseWriter = nil
	ss.flusher = nil
	ss.rc = nil
	if signalLost && ss.clientLost != nil {
		close(ss.clientLost)
	}
	ss.clientLost = nil
}

// Close permanently destroys the session: closes the yamux stream,
// pipes, and signals all goroutines to exit.
func (ss *ShellSession) Close() error {
	if !ss.closed.CompareAndSwap(false, true) {
		return nil // already closed
	}

	ss.mu.Lock()
	ss.clearWriterLocked(false)
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
// previous client. The current attachment's clientLost channel is
// closed so the handler ends the response and the browser reconnects.
// When touchActivity is true, lastActivity is updated (used on heartbeat
// failures to delay idle cleanup). It reports whether it detached and,
// when it did, waits out any in-flight frame write to w so the caller
// can return the HTTP response without a concurrent writer.
func (ss *ShellSession) detachWriter(w io.Writer, touchActivity bool) bool {
	ss.mu.Lock()
	matched := ss.sseWriter == w
	if matched {
		ss.clearWriterLocked(true)
		if touchActivity {
			ss.lastActivity = time.Now()
		}
	}
	ss.mu.Unlock()
	if matched {
		ss.drainFrameWrite()
	}
	return matched
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

// writeAttached emits one SSE frame to the writer snapshot w and enforces
// the detach-on-failure discipline that the output-freeze hardening relies
// on: if the caller's deadline refresh failed (deadlineErr non-nil) or the
// write itself errors, w is detached via detachWriter so the handler ends
// the HTTP response and the browser reconnects. The write runs under frameMu
// so a concurrent takeover cannot drain it mid-frame. kind labels the log
// line ("write error" / "heartbeat error"); touchActivity refreshes the idle
// clock on heartbeat-driven detaches.
func (ss *ShellSession) writeAttached(w io.Writer, deadlineErr error, touchActivity bool, kind string, write func() error) {
	if deadlineErr != nil {
		ss.detachWriter(w, touchActivity)
		return
	}
	// Snapshot writer identity under ss.mu before acquiring frameMu.
	// We cannot hold ss.mu while acquiring frameMu: Attach() holds ss.mu
	// and calls drainFrameWrite() which acquires frameMu, so acquiring
	// both locks here in reversed order would deadlock.
	ss.mu.Lock()
	stale := ss.sseWriter != w
	ss.mu.Unlock()
	if stale {
		return
	}
	ss.frameMu.Lock()
	err := write()
	ss.frameMu.Unlock()
	if err != nil {
		log.Printf("shell: detaching session %s (agent=%s, user=%d): %s: %v", ss.id, ss.agentID, ss.userID, kind, err)
		ss.detachWriter(w, touchActivity)
	}
}

// outputConsumer reads from the yamux stream and either writes SSE
// frames to the attached client or buffers raw bytes into the ring
// buffer when detached. Per-frame write deadlines bound the stall a
// wedged browser can inflict on the consumer, so Close is reachable
// within one heartbeat tick even under TCP backpressure.
//
// Stall-free loop: after sending a heartbeat, the consumer loops back
// to the stream read immediately rather than sleeping an extra tick.
// The previous implementation slept a full heartbeat tick after every
// idle read timeout, which added up to a 15 s echo stall on the first
// keystroke after any idle gap.
func (ss *ShellSession) outputConsumer(heartbeat time.Duration) {
	defer ss.Close()
	defer close(ss.consumerDone)

	buf := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(buf)

	// Track whether we've already logged a ring-overflow warning so we
	// log exactly once per overflow cycle (Drained resets the flag on reattach).
	prevEvicted := false

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
			evicted := ss.ring.Evicted()
			if evicted && !prevEvicted {
				log.Printf("shell: ring buffer overflowed (session=%s, agent=%s), oldest output evicted", ss.id, ss.agentID)
			}
			prevEvicted = evicted
			ss.lastActivity = time.Now()
			closed := ss.closed.Load()
			w := ss.sseWriter
			f := ss.flusher
			var deadlineErr error
			if w != nil && ss.rc != nil {
				deadlineErr = deadlineError(ss.rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline)))
				if deadlineErr != nil {
					log.Printf("shell: set write deadline failed (session=%s, agent=%s): %v", ss.id, ss.agentID, deadlineErr)
				}
			}
			ss.mu.Unlock()

			if closed {
				return
			}

			if w != nil {
				ss.writeAttached(w, deadlineErr, false, "write error", func() error {
					return transport.WriteFrame(w, f, (*buf)[:n])
				})
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
					deadlineErr = deadlineError(rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline)))
					if deadlineErr != nil {
						log.Printf("shell: set write deadline for heartbeat failed (session=%s, agent=%s): %v", ss.id, ss.agentID, deadlineErr)
					}
				}
				ss.writeAttached(w, deadlineErr, true, "heartbeat error", func() error {
					return transport.WriteHeartbeat(w, f)
				})
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
			ss.frameMu.Lock()
			werr := transport.WriteClosedFrame(w, f)
			ss.frameMu.Unlock()
			if werr != nil {
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
	log.Printf("shell: input forwarder started (session=%s, agent=%s)", ss.id, ss.agentID)
	defer log.Printf("shell: input forwarder exited (session=%s, agent=%s)", ss.id, ss.agentID)

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
				log.Printf("shell: input forward failed (session=%s, agent=%s): %v", ss.id, ss.agentID, werr)
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
//
// Transient failures (control stream not yet established, momentary
// write error) are logged but do NOT kill the session — resizes are
// idempotent and the next event will retry.
func (ss *ShellSession) resizeForwarder() {
	defer ss.Close()
	log.Printf("shell: resize forwarder started (session=%s, agent=%s)", ss.id, ss.agentID)
	defer log.Printf("shell: resize forwarder exited (session=%s, agent=%s)", ss.id, ss.agentID)
	for {
		select {
		case ws := <-ss.resizeCh:
			msg := mux.NewResizeMessage(ss.streamID, ws.Cols, ws.Rows)
			// agentSess is always set in production (shellCreate); nil
			// only in tests that exercise the forwarder in isolation.
			if ss.agentSess == nil {
				continue
			}
			if err := ss.agentSess.SendControl(msg); err != nil {
				log.Printf("shell: resize send failed (session=%s, agent=%s): %v", ss.id, ss.agentID, err)
				// Don't return — control stream may not be ready yet;
				// next resize will retry.
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
// than the given idle timeout. It snapshots the session list under the
// registry lock, then inspects state and closes sessions outside it so
// concurrent Store/Load/Delete are not blocked by per-session lock acquisition.
func (r *ShellSessionRegistry) CleanupIdle(timeout time.Duration) {
	now := time.Now()

	// Snapshot all sessions under the lock; inspection happens outside.
	r.mu.Lock()
	all := make([]*ShellSession, 0, len(r.sessions))
	for _, ss := range r.sessions {
		all = append(all, ss)
	}
	r.mu.Unlock()

	var toDelete []string
	var toClose []*ShellSession

	for _, ss := range all {
		dead, attached, lastAct := ss.snapshotState()
		if dead {
			log.Printf("shell: reaping dead session %s (agent=%s)", ss.id, ss.agentID)
			toDelete = append(toDelete, ss.id)
			continue
		}
		if !attached && now.Sub(lastAct) > timeout {
			log.Printf("shell: cleaning up idle session %s (agent=%s, user=%d, idle=%v)", ss.id, ss.agentID, ss.userID, now.Sub(lastAct))
			toDelete = append(toDelete, ss.id)
			toClose = append(toClose, ss)
		}
	}

	if len(toDelete) > 0 {
		r.mu.Lock()
		for _, id := range toDelete {
			delete(r.sessions, id)
		}
		r.mu.Unlock()
	}

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
