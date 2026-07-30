//go:build !windows

package agent

import (
	"bytes"
	"net"
	"strings"
	"testing"
	"time"
)

// newTCPpair creates a connected TCP connection pair for testing.
// Unlike net.Pipe, TCP sockets have kernel buffers that allow the
// proxyShell goroutines to write PTY output without blocking even
// when the test hasn't started reading yet.
func newTCPpair(t *testing.T) (agentSide, testSide net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	ch := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); ch <- c }()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return <-ch, c
}

// readMarker reads from c until marker appears or timeout expires.
func readMarker(t *testing.T, c net.Conn, marker string, timeout time.Duration) string {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(timeout))
	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	for {
		n, err := c.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if strings.Contains(buf.String(), marker) {
			return buf.String()
		}
		if err != nil {
			return buf.String()
		}
	}
}

// TestProxy_ShellTarget_FixedMode verifies that when Agent.Target is
// set to TargetShell, proxy() spawns a PTY shell and pipes I/O
// bidirectionally through the stream.
func TestProxy_ShellTarget_FixedMode(t *testing.T) {
	a := &Agent{Target: TargetShell}

	cAgent, cTest := newTCPpair(t)
	defer cTest.Close()
	defer cAgent.Close()

	done := make(chan struct{})
	go func() {
		a.proxy(cAgent)
		close(done)
	}()

	// Give the shell a moment to start, then send a command.
	time.Sleep(500 * time.Millisecond)

	const marker = "__SHELLFIXED__"
	if _, err := cTest.Write([]byte("echo " + marker + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	output := readMarker(t, cTest, marker, 5*time.Second)
	if !strings.Contains(output, marker) {
		t.Errorf("shell output missing marker; got: %q", output)
	}

	cTest.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not return after stream close")
	}
}

// TestProxy_ShellTarget_DynamicMode verifies that when Agent.Target is
// empty (dynamic mode) and the stream header line is TargetShell,
// proxy() dispatches to proxyShell instead of dialing TCP.
func TestProxy_ShellTarget_DynamicMode(t *testing.T) {
	a := &Agent{Target: ""} // dynamic mode

	cAgent, cTest := newTCPpair(t)
	defer cTest.Close()
	defer cAgent.Close()

	done := make(chan struct{})
	go func() {
		a.proxy(cAgent)
		close(done)
	}()

	// Send the target header line.
	if _, err := cTest.Write([]byte(TargetShell + "\n")); err != nil {
		t.Fatalf("write target header: %v", err)
	}

	// Wait for shell startup.
	time.Sleep(500 * time.Millisecond)

	const marker = "__SHELLDYN__"
	if _, err := cTest.Write([]byte("echo " + marker + "\n")); err != nil {
		t.Fatalf("write command: %v", err)
	}

	output := readMarker(t, cTest, marker, 5*time.Second)
	if !strings.Contains(output, marker) {
		t.Errorf("shell output missing marker; got: %q", output)
	}

	cTest.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not return after stream close")
	}
}
