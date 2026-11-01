package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/mux"
)

// failingSSEWriter fails every data-frame write. SetWriteDeadline succeeds so
// the failure path under test is transport.WriteFrame, not the deadline setup.
type failingSSEWriter struct {
	hdr http.Header
}

func (f *failingSSEWriter) Header() http.Header {
	if f.hdr == nil {
		f.hdr = http.Header{}
	}
	return f.hdr
}
func (f *failingSSEWriter) WriteHeader(int)                  {}
func (f *failingSSEWriter) Flush()                           {}
func (f *failingSSEWriter) SetWriteDeadline(time.Time) error { return nil }
func (f *failingSSEWriter) Write([]byte) (int, error)        { return 0, errors.New("sse write failed") }

// deadlineWriter is an http.ResponseWriter + http.Flusher that captures its
// writes and returns a configured error from SetWriteDeadline. Set deadlineErr
// to http.ErrNotSupported to model a wrapped ResponseWriter that simply cannot
// express deadlines, or to any other error to model a genuine deadline failure.
// The consumer writes from its own goroutine, so the buffer is guarded.
type deadlineWriter struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	deadlineE error
}

func (d *deadlineWriter) Header() http.Header { return http.Header{} }
func (d *deadlineWriter) WriteHeader(int)     {}
func (d *deadlineWriter) Flush()              {}
func (d *deadlineWriter) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.Write(p)
}
func (d *deadlineWriter) SetWriteDeadline(time.Time) error { return d.deadlineE }

// frames returns the number of complete SSE frames written and the decoded
// payload of those frames. Only frames terminated by the SSE blank line are
// decoded, so polling callers never attempt to base64-decode a partial write.
func (d *deadlineWriter) frames(t *testing.T) (int, string) {
	t.Helper()
	d.mu.Lock()
	raw := d.buf.String()
	d.mu.Unlock()
	end := strings.LastIndex(raw, "\n\n")
	if end < 0 {
		return 0, ""
	}
	complete := raw[:end+2]
	return strings.Count(complete, "\n\n"), string(decodeSSEFrames(t, complete))
}

// newShellYamuxPair returns a server-side *yamux.Stream and its peer.
func newShellYamuxPair(t *testing.T) (serverSide, peer *yamux.Stream, closeAll func()) {
	t.Helper()
	c1, c2 := net.Pipe()
	serverSess, err := mux.Server(c1)
	if err != nil {
		t.Fatalf("mux.Server: %v", err)
	}
	clientSess, err := mux.Client(c2)
	if err != nil {
		t.Fatalf("mux.Client: %v", err)
	}
	accepted := make(chan *yamux.Stream, 1)
	go func() {
		s, err := clientSess.AcceptStream()
		if err == nil {
			accepted <- s
		}
	}()
	serverSide, err = serverSess.OpenStream()
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	select {
	case peer = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("peer did not accept stream")
	}
	closeAll = func() {
		serverSide.Close()
		peer.Close()
		serverSess.Close()
		clientSess.Close()
	}
	return serverSide, peer, closeAll
}

func decodeSSEFrames(t *testing.T, raw string) []byte {
	t.Helper()
	var out []byte
	for _, part := range strings.Split(raw, "\n\n") {
		if !strings.HasPrefix(part, "data: ") {
			continue
		}
		dec, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(part, "data: "))
		if err != nil {
			t.Fatalf("decode SSE data: %v", err)
		}
		out = append(out, dec...)
	}
	return out
}

func waitCondition(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestShellSession_ClientWriterFailureKeepsSessionAlive proves that a failed
// SSE write signals the handler (ClientLost) instead of silently parking the
// HTTP response forever, while the persistent shell session and its input
// path stay alive.
func TestShellSession_ClientWriterFailureKeepsSessionAlive(t *testing.T) {
	stream, peer, closeAll := newShellYamuxPair(t)
	defer closeAll()

	ss := NewShellSession(ShellSessionConfig{
		ID: "client-lost", AgentID: "agent-a", UserID: 1, Stream: stream,
	})
	// Long heartbeat keeps the data-write path isolated from heartbeats.
	ss.Start(5 * time.Second)
	defer ss.Close()

	lost, err := ss.Attach(&failingSSEWriter{}, &nopFlusher{}, false)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Output from the agent triggers an SSE write, which fails.
	if _, err := peer.Write([]byte("hello")); err != nil {
		t.Fatalf("peer write: %v", err)
	}
	select {
	case <-lost:
	case <-time.After(3 * time.Second):
		t.Fatal("clientLost not closed after SSE write failure — handler would park forever")
	}
	if ss.IsDead() {
		t.Fatal("session must survive client loss for reattach")
	}
	if ss.Attached() {
		t.Fatal("writer must be cleared after loss")
	}

	// Input path is independent and must keep working after the loss.
	if _, err := ss.UpPipe().Write([]byte("ls\n")); err != nil {
		t.Fatalf("up pipe write: %v", err)
	}
	peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 3)
	if _, err := io.ReadFull(peer, buf); err != nil {
		t.Fatalf("input not forwarded to agent stream after client loss: %v", err)
	}
	if string(buf) != "ls\n" {
		t.Fatalf("input = %q, want %q", buf, "ls\n")
	}

	// Output produced while detached accumulates in the ring buffer.
	if _, err := peer.Write([]byte("after-loss")); err != nil {
		t.Fatalf("peer write: %v", err)
	}
	wantRing := len("hello") + len("after-loss")
	waitCondition(t, 2*time.Second, func() bool { return ss.BufferedBytes() >= wantRing },
		"ring did not capture detached output")

	// Reattach must get a fresh open channel and replay the buffered bytes.
	var replay bytes.Buffer
	lost2, err := ss.Attach(&replay, &nopFlusher{}, false)
	if err != nil {
		t.Fatalf("re-Attach: %v", err)
	}
	if lost2 == nil {
		t.Fatal("re-Attach returned nil loss channel")
	}
	select {
	case <-lost2:
		t.Fatal("re-Attach returned an already-closed loss channel")
	default:
	}
	replayed := decodeSSEFrames(t, replay.String())
	if !bytes.Contains(replayed, []byte("after-loss")) {
		t.Fatalf("replay missing detached output: %q", replay.String())
	}
	// Session death (Close) detaches the writer WITHOUT signalling clientLost:
	// the handler terminates on the Done branch, not the loss branch, so the
	// two teardown paths must stay distinct.
	ss.Close()
	select {
	case <-lost2:
		t.Fatal("Close must not signal clientLost")
	default:
	}
}

// TestShellSession_HeartbeatFailureSignalsClientLost covers the heartbeat
// write path (no agent output at all).
func TestShellSession_HeartbeatFailureSignalsClientLost(t *testing.T) {
	stream, _, closeAll := newShellYamuxPair(t)
	defer closeAll()

	ss := NewShellSession(ShellSessionConfig{
		ID: "hb-lost", AgentID: "agent-a", UserID: 1, Stream: stream,
	})
	ss.Start(50 * time.Millisecond)
	defer ss.Close()

	lost, err := ss.Attach(&failingSSEWriter{}, &nopFlusher{}, false)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	select {
	case <-lost:
	case <-time.After(3 * time.Second):
		t.Fatal("heartbeat write failure did not signal clientLost")
	}
	if ss.IsDead() {
		t.Fatal("session must survive heartbeat write failure")
	}
}

// TestShellSession_DeadlineUnsupportedWriterStaysAttached proves that a writer
// which cannot express write deadlines is not mistaken for a dead client.
// http.ErrNotSupported means "this ResponseWriter has no deadline support" (a
// wrapped or test writer), not "the browser is gone": the deadline exists only
// to bound how long a wedged client can stall the consumer, so an undeadlineable
// writer must still receive both the replayed ring bytes and live output.
func TestShellSession_DeadlineUnsupportedWriterStaysAttached(t *testing.T) {
	stream, peer, closeAll := newShellYamuxPair(t)
	defer closeAll()

	ss := NewShellSession(ShellSessionConfig{
		ID: "no-deadline", AgentID: "agent-a", UserID: 1, Stream: stream,
	})
	ss.Start(5 * time.Second)
	defer ss.Close()

	// Bytes buffered before attach exercise the replay path's deadline setup.
	ss.ring.Write([]byte("replayed"))

	w := &deadlineWriter{deadlineE: http.ErrNotSupported}
	lost, err := ss.Attach(w, w, false)
	if err != nil {
		t.Fatalf("Attach with a deadline-unsupported writer: %v", err)
	}
	// Attach replays the ring synchronously, before it returns.
	if _, got := w.frames(t); !strings.Contains(got, "replayed") {
		t.Fatalf("replayed bytes missing from attach output: %q", got)
	}

	if _, err := peer.Write([]byte("live")); err != nil {
		t.Fatalf("peer write: %v", err)
	}
	waitCondition(t, 2*time.Second, func() bool {
		_, got := w.frames(t)
		return strings.Contains(got, "live")
	}, "live output never reached a deadline-unsupported writer")
	select {
	case <-lost:
		t.Fatal("ErrNotSupported signalled clientLost for a healthy writer")
	default:
	}
	if !ss.Attached() {
		t.Fatal("ErrNotSupported detached a healthy writer")
	}
}

// TestShellSession_HeartbeatDeadlineUnsupportedStaysAttached covers the
// heartbeat write path, which reaches SetWriteDeadline separately from the data
// path. With no agent output at all heartbeats are the only writes, so a writer
// that cannot express deadlines would be detached on every tick without the
// ErrNotSupported guard — a silent reconnect loop on an idle shell.
func TestShellSession_HeartbeatDeadlineUnsupportedStaysAttached(t *testing.T) {
	stream, _, closeAll := newShellYamuxPair(t)
	defer closeAll()

	ss := NewShellSession(ShellSessionConfig{
		ID: "hb-nodeadline", AgentID: "agent-a", UserID: 1, Stream: stream,
	})
	ss.Start(50 * time.Millisecond)
	t.Cleanup(func() { ss.Close() })

	w := &deadlineWriter{deadlineE: http.ErrNotSupported}
	lost, err := ss.Attach(w, w, false)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	// Let several heartbeat ticks elapse with no agent output at all.
	waitCondition(t, 3*time.Second, func() bool {
		n, _ := w.frames(t)
		return n >= 3
	}, "heartbeats never reached a deadline-unsupported writer")
	select {
	case <-lost:
		t.Fatal("heartbeat ErrNotSupported signalled clientLost for a healthy writer")
	default:
	}
	if !ss.Attached() {
		t.Fatal("heartbeat ErrNotSupported detached a healthy writer")
	}
}

// TestShellSession_DeadlineErrorSignalsClientLost keeps the opposite edge: a
// deadline call that fails for a real reason must still detach, so the handler
// ends the response and the browser reconnects.
func TestShellSession_DeadlineErrorSignalsClientLost(t *testing.T) {
	stream, peer, closeAll := newShellYamuxPair(t)
	defer closeAll()

	ss := NewShellSession(ShellSessionConfig{
		ID: "deadline-lost", AgentID: "agent-a", UserID: 1, Stream: stream,
	})
	ss.Start(5 * time.Second)
	defer ss.Close()

	w := &deadlineWriter{deadlineE: io.ErrClosedPipe}
	lost, err := ss.Attach(w, w, false)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := peer.Write([]byte("data")); err != nil {
		t.Fatalf("peer write: %v", err)
	}
	select {
	case <-lost:
	case <-time.After(3 * time.Second):
		t.Fatal("a real deadline error did not signal clientLost")
	}
}

// TestShellSession_TakeoverReplacesStaleClient proves that an explicit
// takeover evicts the previous attachment (whose handler ends its response)
// and that a subsequent failure signals only the new attachment.
func TestShellSession_TakeoverReplacesStaleClient(t *testing.T) {
	ss := &ShellSession{
		id: "takeover-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(64), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}

	var oldWriter, newWriter bytes.Buffer
	lostOld, err := ss.Attach(&oldWriter, &nopFlusher{}, false)
	if err != nil {
		t.Fatalf("Attach old: %v", err)
	}

	// A plain second attach is still rejected.
	if _, err := ss.Attach(&newWriter, &nopFlusher{}, false); err == nil {
		t.Fatal("second Attach without takeover should fail")
	}

	lostNew, err := ss.Attach(&newWriter, &nopFlusher{}, true)
	if err != nil {
		t.Fatalf("takeover Attach: %v", err)
	}
	select {
	case <-lostOld:
	case <-time.After(time.Second):
		t.Fatal("takeover did not evict the previous client")
	}
	select {
	case <-lostNew:
		t.Fatal("takeover returned an already-closed channel")
	default:
	}
	if !ss.Attached() {
		t.Fatal("takeover must install the new writer")
	}

	ss.detachWriter(&newWriter, false)
	select {
	case <-lostNew:
	case <-time.After(time.Second):
		t.Fatal("current writer failure did not signal loss")
	}
	if ss.Attached() {
		t.Fatal("writer must be cleared after current writer failure")
	}
}

// TestShellSession_StaleWriterFailureDoesNotDetachNewClient proves that a
// late failure from a previous attachment cannot clear a newer client's
// writer or signal its loss channel.
func TestShellSession_StaleWriterFailureDoesNotDetachNewClient(t *testing.T) {
	ss := &ShellSession{
		id: "stale-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(64), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}

	var oldWriter, newWriter bytes.Buffer
	if _, err := ss.Attach(&oldWriter, &nopFlusher{}, false); err != nil {
		t.Fatalf("Attach old: %v", err)
	}
	ss.detachWriter(&oldWriter, false)
	lost, err := ss.Attach(&newWriter, &nopFlusher{}, false)
	if err != nil {
		t.Fatalf("Attach new: %v", err)
	}

	ss.detachWriter(&oldWriter, false)
	if !ss.Attached() {
		t.Fatal("stale writer failure detached the new client")
	}
	select {
	case <-lost:
		t.Fatal("stale writer failure signaled the new client's loss channel")
	default:
	}

	ss.detachWriter(&newWriter, false)
	select {
	case <-lost:
	case <-time.After(time.Second):
		t.Fatal("current writer failure did not signal loss")
	}
	if ss.Attached() {
		t.Fatal("writer must be cleared after current writer failure")
	}
}

// TestShellReattach_ReturnsOnClientWriterFailure locks in the handler
// contract: a failed SSE writer ends the handler (response terminates) while
// the persistent session remains available for a later reattach.
func TestShellReattach_ReturnsOnClientWriterFailure(t *testing.T) {
	stream, peer, closeAll := newShellYamuxPair(t)
	defer closeAll()

	ss := NewShellSession(ShellSessionConfig{
		ID: "handler-lost", AgentID: "agent-h", UserID: 1, Stream: stream,
	})
	ss.Start(5 * time.Second)
	t.Cleanup(func() { ss.Close() })

	reg := NewShellSessionRegistry()
	reg.Store(ss)
	h := &Handler{shellSessions: reg, heartbeat: 15 * time.Second}

	sessInfo := &auth.UserSessionInfo{UserID: 1, Role: "admin"}
	req := httptest.NewRequest(http.MethodGet, "/connect?id=conn-1&agent=agent-h&reattach=handler-lost", nil)
	req = req.WithContext(context.WithValue(req.Context(), userSessionKey, sessInfo))
	w := &failingSSEWriter{}

	done := make(chan struct{})
	go func() {
		h.shellReattach(w, req, ss, "conn-1", sessInfo, false)
		close(done)
	}()
	waitCondition(t, 2*time.Second, ss.Attached, "handler did not attach the client")

	// Output triggers the failing write; the handler must return.
	if _, err := peer.Write([]byte("boom")); err != nil {
		t.Fatalf("peer write: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler parked on an open response after client writer failure")
	}
	if got := w.Header().Get("X-SSET-Heartbeat-Ms"); got == "" {
		t.Fatal("X-SSET-Heartbeat-Ms header missing on reattach response")
	}
	if ss.IsDead() {
		t.Fatal("session must survive client loss for reattach")
	}

	// The session is still attachable afterwards.
	var replay bytes.Buffer
	if _, err := ss.Attach(&replay, &nopFlusher{}, false); err != nil {
		t.Fatalf("reattach after client loss: %v", err)
	}
	if !bytes.Contains(decodeSSEFrames(t, replay.String()), []byte("boom")) {
		t.Fatal("reattach replay missing output produced after client loss")
	}
}

// TestShellConnectHandler_TakeoverQueryEvictsStaleClient guards the HTTP→bool
// wiring at ShellConnectHandler (q.Get("takeover") == "1"). The session-level
// takeover path is covered elsewhere; this proves a ?takeover=1 reattach
// request actually evicts an occupying client, while a plain reattach does
// not. A watchdog-stalled browser depends on this to reclaim a half-open
// attachment, so a regression here reintroduces the output-freeze class.
func TestShellConnectHandler_TakeoverQueryEvictsStaleClient(t *testing.T) {
	stream, _, closeAll := newShellYamuxPair(t)
	defer closeAll()

	// Long heartbeat keeps the consumer from writing during the test, so the
	// recorder-backed attachment is stable (no write/deadline path taken).
	ss := NewShellSession(ShellSessionConfig{
		ID: "tk-1", AgentID: "agent-tk", UserID: 1, Stream: stream,
	})
	ss.Start(time.Minute)
	t.Cleanup(func() { ss.Close() })

	reg := NewShellSessionRegistry()
	reg.Store(ss)
	h := &Handler{shellSessions: reg, heartbeat: time.Minute}
	sessInfo := &auth.UserSessionInfo{UserID: 1, Role: "admin"}

	shellReq := func(query string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/connect?"+query, nil)
		return req.WithContext(context.WithValue(req.Context(), userSessionKey, sessInfo))
	}

	// First client occupies the writer (no reattach id needed — the registry
	// finds this session for the user+agent pair).
	done1 := make(chan struct{})
	w1 := httptest.NewRecorder()
	go func() { h.ShellConnectHandler().ServeHTTP(w1, shellReq("id=c1&agent=agent-tk")); close(done1) }()
	waitCondition(t, 3*time.Second, ss.Attached, "first client did not attach")

	// A second client WITHOUT takeover must be refused ("already attached")
	// and leave the first client's handler parked.
	w2 := httptest.NewRecorder()
	h.ShellConnectHandler().ServeHTTP(w2, shellReq("id=c2&agent=agent-tk&reattach=tk-1"))
	select {
	case <-done1:
		t.Fatal("plain reattach evicted the first client (takeover not requested)")
	default:
	}

	// The same request WITH takeover=1 evicts the stale first client, so its
	// handler ends the response (done1 closes).
	go h.ShellConnectHandler().ServeHTTP(httptest.NewRecorder(), shellReq("id=c3&agent=agent-tk&reattach=tk-1&takeover=1"))
	select {
	case <-done1:
	case <-time.After(3 * time.Second):
		t.Fatal("takeover=1 query did not evict the stale first client")
	}
	if !ss.Attached() {
		t.Fatal("takeover client should be attached after evicting the stale one")
	}
}

// shellConnectRequest builds a GET /connect request carrying an authenticated
// user session, so handler tests can drive ShellConnectHandler directly.
func shellConnectRequest(query string, sessInfo *auth.UserSessionInfo) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/connect?"+query, nil)
	return req.WithContext(context.WithValue(req.Context(), userSessionKey, sessInfo))
}

// TestShellConnectHandler_BusySessionReturnsConflict proves a second client
// that tries to attach an already-attached session is refused with 409 Conflict
// BEFORE the SSE stream begins, instead of getting a 200 with an empty body.
// The empty 200 looked to the browser like a recoverable stream end, so it
// auto-reconnected; the give-up path then reset the counter, and two tabs on
// one user+agent pair hammered /connect forever (logging an attach failure
// every ~1 s) while neither could see the other.
func TestShellConnectHandler_BusySessionReturnsConflict(t *testing.T) {
	stream, _, closeAll := newShellYamuxPair(t)
	defer closeAll()

	// Long heartbeat keeps the consumer from writing, so client A's
	// attachment is stable for the duration of the test.
	ss := NewShellSession(ShellSessionConfig{
		ID: "busy-1", AgentID: "agent-b", UserID: 1, Stream: stream,
	})
	ss.Start(time.Minute)
	t.Cleanup(func() { ss.Close() })

	reg := NewShellSessionRegistry()
	reg.Store(ss)
	h := &Handler{shellSessions: reg, heartbeat: time.Minute}
	sessInfo := &auth.UserSessionInfo{UserID: 1, Role: "admin"}

	// Client A holds the attachment.
	wA := httptest.NewRecorder()
	go h.ShellConnectHandler().ServeHTTP(wA, shellConnectRequest("id=a&agent=agent-b", sessInfo))
	waitCondition(t, 3*time.Second, ss.Attached, "client A did not attach")

	// Client B reattaches the same session without takeover.
	wB := httptest.NewRecorder()
	h.ShellConnectHandler().ServeHTTP(wB, shellConnectRequest("id=b&agent=agent-b&reattach=busy-1", sessInfo))

	if wB.Code != http.StatusConflict {
		t.Fatalf("busy status = %d, want %d", wB.Code, http.StatusConflict)
	}
	if ct := wB.Header().Get("Content-Type"); ct == "text/event-stream" {
		t.Fatal("busy refusal must not begin the SSE stream")
	}
	// The refusal must not disturb the client that actually holds the writer.
	if !ss.Attached() {
		t.Fatal("busy refusal detached the current client")
	}
}

// TestShellSession_ConcurrentTakeoverNoDeadlock exercises the race between
// writeAttached (which snapshots writer identity under ss.mu, then acquires
// frameMu) and Attach(takeover=true) (which holds ss.mu and calls
// drainFrameWrite). A deadlock here would be a lock-ordering inversion.
func TestShellSession_ConcurrentTakeoverNoDeadlock(t *testing.T) {
	ss := &ShellSession{
		id: "race-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(64), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}

	// slowWriter simulates a slow SSE write that holds frameMu for a while.
	type slowWriter struct {
		bytes.Buffer
		delay time.Duration
	}
	sw := func(delay time.Duration) *slowWriter {
		return &slowWriter{delay: delay}
	}
	writeFn := func(sw *slowWriter) func() error {
		return func() error {
			time.Sleep(sw.delay)
			_, err := sw.Write([]byte("data: test\n\n"))
			return err
		}
	}

	// Phase 1: attach client A with a slow writer.
	wA := sw(10 * time.Millisecond)
	_, err := ss.Attach(wA, &nopFlusher{}, false)
	if err != nil {
		t.Fatalf("Attach A: %v", err)
	}

	// Phase 2: run writeAttached and takeover in separate goroutines so
	// they truly overlap — writeAttached holds frameMu while Attach holds
	// ss.mu and calls drainFrameWrite. This is the only way to exercise
	// the lock-ordering inversion.
	const iterations = 50
	writerDone := make(chan struct{})
	takeoverDone := make(chan struct{})

	// Writer goroutine: simulates outputConsumer.
	go func() {
		defer close(writerDone)
		for i := 0; i < iterations; i++ {
			ss.mu.Lock()
			w := ss.sseWriter
			ss.mu.Unlock()
			if w != nil {
				ss.writeAttached(w, nil, false, "test write", writeFn(sw(5*time.Millisecond)))
			}
		}
	}()

	// Takeover goroutine: simulates concurrent Attach(takeover).
	go func() {
		defer close(takeoverDone)
		for i := 0; i < iterations; i++ {
			newW := sw(1 * time.Millisecond)
			_, err := ss.Attach(newW, &nopFlusher{}, true)
			if err != nil {
				// Session may have been closed; that's OK.
				return
			}
		}
	}()

	// Verify no deadlock within a generous timeout.
	timeout := time.After(10 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-writerDone:
		case <-takeoverDone:
		case <-timeout:
			t.Fatal("deadlock detected: concurrent writeAttached + Attach(takeover) did not complete")
		}
	}

	// Verify the session is still attached (last takeover succeeded).
	if !ss.Attached() {
		t.Fatal("session should still be attached after takeovers")
	}
}

// failingAfterNWriter succeeds for the first n bytes then fails. Used to
// simulate a mid-replay write failure so the ring re-write path is exercised.
type failingAfterNWriter struct {
	buf       bytes.Buffer
	remaining int
}

func (f *failingAfterNWriter) Header() http.Header { return http.Header{} }
func (f *failingAfterNWriter) Flush()              {}
func (f *failingAfterNWriter) Write(p []byte) (int, error) {
	if f.remaining <= 0 {
		return 0, errors.New("write failed after N bytes")
	}
	n := len(p)
	if n > f.remaining {
		// Write up to the limit, then fail.
		f.buf.Write(p[:f.remaining])
		f.remaining = 0
		return f.remaining, errors.New("write failed after N bytes")
	}
	f.buf.Write(p)
	f.remaining -= n
	return n, nil
}

// TestShellSession_ReplayFailureReWritesToRing verifies that when a replay
// write fails mid-stream, the unsent tail is re-written to the ring buffer
// so a subsequent reattach can replay it.
func TestShellSession_ReplayFailureReWritesToRing(t *testing.T) {
	const payload = "HELLO WORLD 1234567890 ABCDEF"
	ss := &ShellSession{
		id: "replay-rewind-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(4096), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}

	// Pre-fill the ring with known data.
	ss.ring.Write([]byte(payload))

	// Attach with a writer that fails after the first chunk. replayChunkSize
	// is 64 KiB, so even a small payload will be in one chunk. Use a writer
	// that fails immediately to exercise the failure path.
	w := &failingAfterNWriter{remaining: 0}
	_, err := ss.Attach(w, &nopFlusher{}, false)
	if err == nil {
		t.Fatal("expected replay failure, got nil")
	}

	// The unsent data should be back in the ring.
	ringLen := ss.ring.Len()
	if ringLen == 0 {
		t.Fatal("ring is empty after replay failure — unsent data was lost")
	}

	// Reattach with a good writer and verify the original payload appears.
	var buf bytes.Buffer
	lost, err := ss.Attach(&buf, &nopFlusher{}, true)
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	_ = lost

	// The replay should include the original payload (base64-encoded in SSE frame).
	got := buf.String()
	if !strings.HasPrefix(got, "data: ") || !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("replay is not a single SSE data frame: %q", got)
	}
	encoded := strings.TrimPrefix(strings.TrimSuffix(got, "\n\n"), "data: ")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode base64 payload: %v", err)
	}
	if !bytes.Contains(decoded, []byte(payload)) {
		t.Errorf("reattach replay missing original payload; decoded=%q", decoded)
	}
}

// TestHandleConnectResize_NilChannel_Returns400 verifies that resize requests
// targeting non-shell connect sessions (resize == nil) are rejected with 400
// rather than silently dropped with 200.
func TestHandleConnectResize_NilChannel_Returns400(t *testing.T) {
	h := &Handler{}

	// Store a connect session with nil resize channel (non-shell).
	// userID=0 skips the user-session-required check.
	h.connectSessions.Store("test-conn", &connectSession{
		agentID: "agent-1",
		userID:  0,
		resize:  nil,
	})

	body := `{"id":"test-conn","cols":80,"rows":24}`
	req := httptest.NewRequest(http.MethodPost, "/shell/resize", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleConnectResize(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for nil resize channel, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "resize not supported") {
		t.Errorf("expected error message about resize not supported, got %q", rec.Body.String())
	}
}
