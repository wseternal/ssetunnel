//go:build !windows

package agent

import (
	"bytes"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/creack/pty"
)

// writeResize encodes a resize message in the new length-prefixed wire
// format: 0x00 | uint16be(len) | JSON. Helper shared by the tests below.
func writeResize(t *testing.T, c net.Conn, cols, rows uint16) {
	t.Helper()
	msg, _ := json.Marshal(struct {
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}{cols, rows})
	if len(msg) > 0xFFFF {
		t.Fatalf("resize payload too large: %d", len(msg))
	}
	frame := make([]byte, 3+len(msg))
	frame[0] = 0
	frame[1] = byte(len(msg) >> 8)
	frame[2] = byte(len(msg))
	copy(frame[3:], msg)
	if _, err := c.Write(frame); err != nil {
		t.Fatalf("write resize: %v", err)
	}
}

// TestResizeProtocol_LengthPrefixed verifies the agent parses the new
// length-prefixed resize wire format (0x00 | uint16be(len) | JSON) and
// applies it to the PTY.
func TestResizeProtocol_LengthPrefixed(t *testing.T) {
	// Allocate a PTY so Setsize has something to operate on.
	ptmx, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer ptmx.Close()
	defer slave.Close()

	// TCP socket pair for the "stream" side.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	ch := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); ch <- c }()
	stream, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	agentSide := <-ch
	defer stream.Close()
	defer agentSide.Close()

	done := make(chan struct{})
	go func() {
		forwardStreamToPTY(agentSide, ptmx)
		close(done)
	}()

	// Send a resize frame with a specific size.
	writeResize(t, stream, 120, 40)

	// Give the agent a moment to apply the resize.
	time.Sleep(100 * time.Millisecond)

	rows, cols, err := pty.Getsize(ptmx)
	if err != nil {
		t.Fatalf("Getsize: %v", err)
	}
	if cols != 120 || rows != 40 {
		t.Fatalf("resize not applied: got cols=%d rows=%d, want 120x40", cols, rows)
	}

	// Close the stream — the forwarder should return cleanly.
	stream.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forwardStreamToPTY did not return after stream close")
	}
}

// TestResizeProtocol_SpuriousNUL verifies that a stray NUL byte (as
// emitted by xterm.js for Ctrl-Space) does not swallow subsequent
// keystrokes. With the old newline-terminated protocol, one stray NUL
// would buffer every following byte until a literal 0x0A happened by
// accident. The length-prefixed protocol bounds the cost to a handful
// of dropped bytes and keeps the input stream usable.
func TestResizeProtocol_SpuriousNUL(t *testing.T) {
	ptmx, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer ptmx.Close()
	defer slave.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	ch := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); ch <- c }()
	stream, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	agentSide := <-ch
	defer stream.Close()
	defer agentSide.Close()

	done := make(chan struct{})
	go func() {
		forwardStreamToPTY(agentSide, ptmx)
		close(done)
	}()

	// Inject a stray NUL followed immediately by normal keystrokes.
	// The old protocol would swallow "hello" into the resize parser
	// indefinitely. The new protocol reads 2 length bytes (here 'H',
	// 'E'), treats the length as implausible (>maxResizePayload), and
	// tries to discard up to maxResizePayload bytes under a 2 s read
	// deadline. That discard consumes the remaining "LLO\n" plus any
	// bytes we send next, until either 1024 bytes are consumed or the
	// deadline fires. After the deadline, readAndApplyResize returns
	// and the forwarder goes back to normal mode.
	if _, err := stream.Write([]byte{0x00}); err != nil {
		t.Fatalf("write NUL: %v", err)
	}
	// Give the agent a moment to enter readAndApplyResize.
	time.Sleep(50 * time.Millisecond)
	// First batch: consumed entirely by the false trigger's bounded
	// discard (length decodes to a large value, so it tries to read
	// 1024 bytes; this batch is part of that 1024).
	if _, err := stream.Write([]byte("HELLO\n")); err != nil {
		t.Fatalf("write keystrokes: %v", err)
	}
	// Wait for the 2 s deadline to expire inside io.CopyN. Then send
	// fresh data — the forwarder must have returned to normal mode and
	// deliver this to the PTY.
	time.Sleep(2500 * time.Millisecond)
	if _, err := stream.Write([]byte("RECOVER\n")); err != nil {
		t.Fatalf("write recovery keystrokes: %v", err)
	}

	// Read PTY output with a deadline. The echo of "RECOVER" must
	// appear, proving the input stream did not lock up permanently
	// after the stray NUL.
	slave.SetReadDeadline(time.Now().Add(4 * time.Second))
	var buf [256]byte
	var got bytes.Buffer
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		n, err := slave.Read(buf[:])
		if n > 0 {
			got.Write(buf[:n])
		}
		if bytes.Contains(got.Bytes(), []byte("RECOVER")) {
			break
		}
		if err != nil {
			if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
				continue
			}
			break
		}
	}
	if !bytes.Contains(got.Bytes(), []byte("RECOVER")) {
		t.Fatalf("input stream did not recover after stray NUL: got %q, want substring RECOVER", got.String())
	}

	stream.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forwardStreamToPTY did not return after stream close")
	}
}

// TestResizeProtocol_BoundedFalseTrigger proves that a stray NUL whose
// 2-byte length field decodes to an implausible value (>maxResizePayload)
// does not block the input goroutine indefinitely. The 2 s read
// deadline inside readAndApplyResize guarantees forward progress even
// when the stream has no more bytes to offer.
func TestResizeProtocol_BoundedFalseTrigger(t *testing.T) {
	ptmx, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer ptmx.Close()
	defer slave.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	ch := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); ch <- c }()
	stream, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	agentSide := <-ch
	defer stream.Close()
	defer agentSide.Close()

	done := make(chan struct{})
	go func() {
		forwardStreamToPTY(agentSide, ptmx)
		close(done)
	}()

	// Inject a NUL followed by a length that claims 0xFFFF bytes, then
	// nothing. The old protocol would hang forever waiting for '\n'.
	// The new protocol either rejects the length (n > maxResizePayload)
	// and consumes up to maxResizePayload bytes under a 2 s deadline,
	// or succeeds within 2 s. Either way, forwardStreamToPTY must
	// return when the stream closes after the test timeout.
	if _, err := stream.Write([]byte{0x00, 0xFF, 0xFF}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Wait longer than the 2 s read deadline but less than the test
	// timeout, then close the stream. The forwarder must return.
	time.Sleep(3 * time.Second)
	stream.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("forwardStreamToPTY did not recover from false NUL trigger")
	}
}
