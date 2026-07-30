//go:build windows

package agent

import (
	"log"
	"net"
)

// proxyShell is not supported on Windows. Closes the stream and logs
// an error. Cloud shell requires a Unix PTY.
func (a *Agent) proxyShell(stream net.Conn) {
	log.Printf("agent: shell target not supported on Windows")
	stream.Close()
}
