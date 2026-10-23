package server

import (
	"bytes"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/mux"
)

// TestShellSession_DoneClosedAfterClose proves the P1 invariant:
// closing the session closes ss.Done(), which is the signal the HTTP
// handler selects on to terminate the SSE response when the backing
// session dies. Without this property, the handler parks on
// r.Context().Done() forever and the browser freezes silently.
func TestShellSession_DoneClosedAfterClose(t *testing.T) {
	ss := &ShellSession{
		id: "done-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(64), wakeup: make(chan struct{}, 1), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}
	// Pre-condition: Done is open.
	select {
	case <-ss.Done():
		t.Fatal("Done should be open before Close")
	default:
	}
	if err := ss.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Post-condition: Done is closed within a short window.
	select {
	case <-ss.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed after Close — browser would freeze")
	}
}

// TestShellSession_AttachNoByteSkip verifies the F6 invariant: Attach
// does not skip bytes that arrive in the ring between the drain and
// the writer install. The previous implementation had a lock-release-
// reacquire gap where bytes went to the ring but were never replayed,
// producing corrupt terminal state mid-escape-sequence on reattach.
//
// With the atomic single-lock Attach, the writer is installed before
// the ring is drained, so bytes arriving after drain see an installed
// writer and go straight to it — no bytes are ever lost to the ring.
func TestShellSession_AttachNoByteSkip(t *testing.T) {
	ss := &ShellSession{
		id: "atomic-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(256), wakeup: make(chan struct{}, 1), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}
	// Pre-populate the ring with known bytes.
	ss.ring.Write([]byte("PRE-ATTACH"))

	var buf bytes.Buffer
	if err := ss.Attach(&buf, &nopFlusher{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Ring must be drained by Attach.
	if ss.ring.Len() != 0 {
		t.Fatalf("ring Len = %d after Attach, want 0", ss.ring.Len())
	}
	// Replay is written as an SSE data frame with base64-encoded payload:
	//   data: <base64(PRE-ATTACH)>\n\n
	// Decode the frame and verify the payload bytes appear inside it.
	got := buf.String()
	if !strings.HasPrefix(got, "data: ") || !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("replay is not a single SSE data frame: %q", got)
	}
	encoded := strings.TrimPrefix(strings.TrimSuffix(got, "\n\n"), "data: ")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode base64 payload: %v", err)
	}
	if !bytes.Contains(decoded, []byte("PRE-ATTACH")) {
		t.Fatalf("replay did not include pre-attach bytes; decoded=%q", decoded)
	}
}

// TestShellSession_ResizeForwarderClosesSession proves the A6 invariant:
// a resize write failure terminates the session instead of leaving
// resize handling silently dead for the session's remaining life.
//
// The previous implementation returned from resizeForwarder on error
// without calling ss.Close(), so a single transient stream error
// killed resize handling permanently while the other forwarders kept
// working and resize POSTs still returned 200.
func TestShellSession_ResizeForwarderClosesSession(t *testing.T) {
	// Build a yamux stream pair and close the server side so any write
	// from the agent side fails immediately.
	c1, c2 := net.Pipe()
	defer c2.Close()
	serverSess, err := mux.Server(c1)
	if err != nil {
		t.Fatalf("mux.Server: %v", err)
	}
	clientSess, err := mux.Client(c2)
	if err != nil {
		t.Fatalf("mux.Client: %v", err)
	}
	defer clientSess.Close()
	// Accept on the server side to complete the handshake, then open
	// a stream from the client to use as the shell session's stream.
	go func() { _, _ = serverSess.Accept() }()
	stream, err := clientSess.OpenStream()
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	// Close the server session so the agent-side stream is broken.
	serverSess.Close()

	ss := &ShellSession{
		id: "resize-close-1", agentID: "a", userID: 1,
		stream:   stream,
		ring:     NewRingBuffer(64),
		wakeup:   make(chan struct{}, 1),
		done:     make(chan struct{}),
		resizeCh: make(chan windowSize, 1),
		createdAt: time.Now(), lastActivity: time.Now(),
	}
	go ss.resizeForwarder()
	ss.resizeCh <- windowSize{Cols: 80, Rows: 24}
	select {
	case <-ss.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("resizeForwarder did not close session on write error — A6 regression")
	}
}
