//go:build !windows

package agent

import (
	"io"
	"log"
	"net"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// proxyShell spawns an interactive shell with a PTY and proxies
// bidirectionally between the yamux stream and the shell's stdin/stdout.
// When the stream closes or the shell exits, both sides are torn down.
func (a *Agent) proxyShell(stream net.Conn) {
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
	defer func() {
		ptmx.Close()
		cmd.Wait() // reap the shell process
	}()

	log.Printf("agent: shell session started (pid=%d, shell=%s)", cmd.Process.Pid, shell)

	// stream → PTY (user input). When the stream closes, close the
	// PTY write side so the shell sees EOF on stdin.
	go func() {
		io.Copy(ptmx, stream)
		ptmx.Close() // signal EOF to shell
	}()

	// PTY → stream (shell output). When the shell exits or the PTY
	// closes, close the stream so the connect client sees EOF.
	go func() {
		io.Copy(stream, ptmx)
		stream.Close()
	}()

	// Wait for either side to finish. We don't need explicit waiting
	// here because the goroutines above handle cleanup via defer.
	// The cmd.Wait() in the deferred func reaps the process.
}
