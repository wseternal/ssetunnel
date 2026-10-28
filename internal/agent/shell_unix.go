//go:build !windows

package agent

import (
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/hashicorp/yamux"
	"github.com/wseternal/ssetunnel/internal/mux"
)

// proxyShell spawns an interactive shell with a PTY and proxies
// bidirectionally between the yamux data stream and the shell's
// stdin/stdout. The data stream is a pure byte pipe — resize commands
// arrive via the separate control stream (not interleaved on the data
// stream). streamID is the yamux stream ID for control stream routing.
// agent provides the stream registry for control message routing.
func proxyShell(stream net.Conn, streamID uint32, agent *Agent) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell)
	// Prefix argv[0] with '-' to invoke a login shell. This causes
	// zsh/bash to source /etc/profile, ~/.zprofile (or ~/.profile),
	// matching the behavior of sshd and login(1). Without the '-'
	// prefix, only interactive startup files (.zshrc/.bashrc) are
	// sourced, so PATH additions from .zprofile are missing.
	cmd.Args[0] = "-" + filepath.Base(shell)
	// Build a clean environment with an explicit TERM=xterm-256color.
	// pty.Start appends TERM=xterm, but if os.Environ() already contains
	// a TERM entry (e.g. from the agent's parent process), the duplicate
	// may cause the shell to use the first (possibly wrong) value, which
	// can prevent ZLE from activating properly.
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TERM=") {
			env = append(env, e)
		}
	}
	env = append(env, "TERM=xterm-256color")
	cmd.Env = env

	// Start the command with a PTY. The returned file is the master
	// side of the pseudo-terminal — reads yield shell output, writes
	// feed shell input.
	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("agent: start shell %s: %v", shell, err)
		stream.Close()
		return
	}

	log.Printf("agent: shell session started (pid=%d, shell=%s, stream=%d)", cmd.Process.Pid, shell, streamID)

	// Register this data stream with the agent's control dispatcher
	// so resize commands from the control stream are routed here.
	sh := &shellHandler{
		ptmx:     ptmx,
		streamID: streamID,
		resizeCh: make(chan resizeRequest, 16),
	}
	if agent != nil && agent.registry != nil {
		agent.registry.Register(streamID, sh)
		defer agent.registry.Unregister(streamID)
	}

	// Start the resize applier goroutine.
	go sh.resizeLoop()

	var wg sync.WaitGroup
	wg.Add(2)

	// stream → PTY (user input). Pure byte pipe — no control message
	// parsing needed. When the stream closes, close the PTY master to
	// signal EOF to the shell. If the shell doesn't exit within 2
	// seconds (e.g., traps SIGHUP), force-kill it.
	go func() {
		defer wg.Done()
		io.Copy(ptmx, stream)
		ptmx.Close() // signal EOF to shell (sends SIGHUP)

		// Give the shell 2 seconds to exit gracefully, then force-kill.
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			cmd.Process.Kill() // force-kill: closes slave FD → unblocks output goroutine
		}
	}()

	// PTY → stream (shell output). When the shell exits or the PTY
	// closes, close the stream so the connect client sees EOF.
	go func() {
		defer wg.Done()
		io.Copy(stream, ptmx)
		stream.Close()
	}()

	wg.Wait()

	// Reap the shell process. It should already be dead (exited
	// naturally from EOF or force-killed by the input goroutine).
	cmd.Wait()
}

// resizeRequest carries a PTY resize from the control dispatcher to
// the resize applier goroutine.
type resizeRequest struct {
	cols, rows uint16
}

// shellHandler implements RegisteredStream for a shell data stream.
// It receives resize commands from the control dispatcher and applies
// them to the PTY via a buffered channel + applier goroutine (so
// resize writes don't block the control reader).
type shellHandler struct {
	ptmx     *os.File
	streamID uint32
	resizeCh chan resizeRequest
}

// HandleResize enqueues a resize request for the applier goroutine.
// Non-blocking: drops if the channel buffer is full.
func (sh *shellHandler) HandleResize(cols, rows uint16) {
	select {
	case sh.resizeCh <- resizeRequest{cols: cols, rows: rows}:
	default:
		// Buffer full — drop oldest and enqueue new.
		select {
		case <-sh.resizeCh:
		default:
		}
		sh.resizeCh <- resizeRequest{cols: cols, rows: rows}
	}
	// No trace here: the resize originated from the server, which
	// already knows it was sent. Tracing it would produce a redundant
	// round-trip on every resize event.
}

// HandleTrace is a no-op for shell sessions — shell sessions don't
// receive trace events from the server.
func (sh *shellHandler) HandleTrace(event mux.TraceEvent) {
	// No-op: shell sessions don't receive trace events.
}

// resizeLoop drains resize requests and applies them to the PTY.
func (sh *shellHandler) resizeLoop() {
	for req := range sh.resizeCh {
		if err := pty.Setsize(sh.ptmx, &pty.Winsize{
			Rows: req.rows,
			Cols: req.cols,
		}); err != nil {
			log.Printf("agent: pty resize (stream=%d): %v", sh.streamID, err)
		}
	}
}

// getStreamID extracts the yamux stream ID from a net.Conn, which may
// be wrapped in a readerConn. Returns 0 if the underlying type is not
// a yamux stream.
func getStreamID(stream net.Conn) uint32 {
	if rc, ok := stream.(*readerConn); ok {
		if ys, ok := rc.Conn.(*yamux.Stream); ok {
			return ys.StreamID()
		}
	}
	if ys, ok := stream.(*yamux.Stream); ok {
		return ys.StreamID()
	}
	return 0
}
