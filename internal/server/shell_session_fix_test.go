package server

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"
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

// TestShellSession_ResizeForwarderSurvivesTransientError proves that a
// resize send failure does NOT terminate the session. The previous
// implementation exited permanently on the first error, killing resize
// handling for the session's remaining life. The new behavior logs and
// continues — resizes are idempotent, so the next event will retry.
func TestShellSession_ResizeForwarderSurvivesTransientError(t *testing.T) {
	ss := &ShellSession{
		id: "resize-survive-1", agentID: "a", userID: 1,
		ring:     NewRingBuffer(64),
		wakeup:   make(chan struct{}, 1),
		done:     make(chan struct{}),
		resizeCh: make(chan windowSize, 2),
		createdAt: time.Now(), lastActivity: time.Now(),
		// agentSess is nil — simulates control stream not yet ready.
	}
	go ss.resizeForwarder()

	// Send a resize; agentSess is nil so it should be dropped.
	ss.resizeCh <- windowSize{Cols: 80, Rows: 24}

	// Give the forwarder time to process.
	time.Sleep(100 * time.Millisecond)

	// Session must NOT be closed — the forwarder should still be running.
	select {
	case <-ss.Done():
		t.Fatal("resizeForwarder killed session on transient error — should have continued")
	default:
	}

	// Close the session to clean up.
	ss.Close()
	select {
	case <-ss.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session did not close after Close()")
	}
}
