//go:build !windows

package agent

import (
	"io"
	"log"
	"net"
	"os"
	"os/exec"
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
	cmd.Env = os.Environ()

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

	// stream → PTY (user input). When the stream closes, close the
	// PTY master to signal EOF to the shell. If the shell doesn't
	// exit within 2 seconds (e.g., traps SIGHUP), force-kill it.
	// Killing the process closes the slave FD, which causes the PTY
	// master read in the output goroutine to return EIO, unblocking
	// it and allowing wg.Wait() to return.
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
