//go:build !windows

package agent

import (
	"encoding/json"
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
)

// proxyShell spawns an interactive shell with a PTY and proxies
// bidirectionally between the yamux stream and the shell's stdin/stdout.
// When the stream closes or the shell exits, both sides are torn down.
func proxyShell(stream net.Conn) {
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

	log.Printf("agent: shell session started (pid=%d, shell=%s)", cmd.Process.Pid, shell)

	var wg sync.WaitGroup
	wg.Add(2)

	// stream → PTY (user input + resize commands). The server multiplexes
	// regular keystrokes and length-prefixed JSON resize messages on the
	// same stream. The wire format is:
	//   0x00 | uint16be(len) | JSON payload
	// The previous newline-terminated format (0x00 JSON '\n') was
	// ambiguous with user-generated NUL bytes: xterm.js emits \x00 for
	// Ctrl-Space and Ctrl-@, and in a raw PTY Enter produces \r not \n,
	// so a stray NUL would previously swallow all subsequent keystrokes
	// into an unbounded buffer until a literal 0x0A happened by
	// accident. The 2-byte length bounds the cost of a false trigger to
	// at most a handful of dropped bytes.
	//
	// When the stream closes, close the PTY master to signal EOF to the
	// shell. If the shell doesn't exit within 2 seconds (e.g., traps
	// SIGHUP), force-kill it. Killing the process closes the slave FD,
	// which causes the PTY master read in the output goroutine to return
	// EIO, unblocking it and allowing wg.Wait() to return.
	go func() {
		defer wg.Done()
		forwardStreamToPTY(stream, ptmx)
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

// forwardStreamToPTY reads from the yamux stream one byte at a time and
// writes to the PTY master. NUL bytes (\x00) signal out-of-band resize
// commands: the next 2 bytes are a big-endian uint16 length, followed
// by that many bytes of JSON {cols, rows} applied via pty.Setsize. The
// length prefix bounds the read so a stray NUL from user input
// (Ctrl-Space, Ctrl-@) costs at most a few dropped bytes instead of
// swallowing keystrokes until '\n'.
//
// False-trigger handling: if the length is implausibly large, or the
// payload is not JSON (doesn't start with '{'), the bytes are treated
// as user input that happened to follow a NUL and are discarded rather
// than applied as a resize. Subsequent resize events from the server
// correct the PTY size, so any transient corruption from a false
// trigger self-heals on the next real resize.
func forwardStreamToPTY(stream net.Conn, ptmx *os.File) {
	header := make([]byte, 3) // 0x00 + 2-byte length
	for {
		if _, err := io.ReadFull(stream, header[:1]); err != nil {
			return
		}
		if header[0] != 0 {
			if _, err := ptmx.Write(header[:1]); err != nil {
				return
			}
			continue
		}
		if !readAndApplyResize(stream, ptmx) {
			return
		}
	}
}

// maxResizePayload bounds the resize JSON payload. Real resize messages
// are ~25 bytes ("{\"cols\":NNN,\"rows\":NNN}"). 1 KiB is generous headroom
// while still rejecting implausible lengths from a false NUL trigger.
const maxResizePayload = 1024

// readAndApplyResize reads a length-prefixed JSON resize payload from
// the stream and applies it to the PTY. Returns false if the stream
// closes or a read deadline expires. A malformed payload (wrong length
// or not JSON) is discarded silently — the next real resize from the
// server will correct the PTY size.
func readAndApplyResize(stream net.Conn, ptmx *os.File) bool {
	// Bound the entire resize sequence so a stray NUL followed by an
	// idle stream cannot block the input goroutine forever.
	stream.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer stream.SetReadDeadline(time.Time{}) // clear deadline on all exit paths

	var lenBuf [2]byte
	if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
		return false
	}
	n := int(lenBuf[0])<<8 | int(lenBuf[1])
	if n == 0 || n > maxResizePayload {
		// Implausible length — false NUL trigger. Discard whatever
		// bytes we can consume (up to maxResizePayload) to keep the
		// stream aligned with the next real frame, then return
		// success so the caller keeps reading.
		if n > maxResizePayload {
			n = maxResizePayload
		}
		if n > 0 {
			_, _ = io.CopyN(io.Discard, stream, int64(n))
		}
		return true
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(stream, buf); err != nil {
		return false
	}
	// Sanity check: payload must be JSON object.
	if len(buf) == 0 || buf[0] != '{' {
		return true // false trigger; discard and continue
	}
	var ws struct {
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}
	if err := json.Unmarshal(buf, &ws); err != nil {
		log.Printf("agent: bad resize JSON: %v", err)
		return true
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{
		Rows: ws.Rows,
		Cols: ws.Cols,
	}); err != nil {
		log.Printf("agent: pty resize: %v", err)
	}
	return true
}
