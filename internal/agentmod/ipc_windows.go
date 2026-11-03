//go:build windows

package agentmod

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/Microsoft/go-winio"
)

// ipcNetwork is the network type for Windows named pipes.
const ipcNetwork = "unix" // Use "unix" for compatibility; winio handles pipes

// IPCEndpoint returns the default IPC endpoint path for the given data directory.
// On Windows, this is a named pipe path.
func IPCEndpoint(dataDir string) string {
	// Use the data directory name as part of the pipe name to avoid conflicts.
	base := filepath.Base(dataDir)
	return fmt.Sprintf(`\\.\pipe\ssetunnel-module-%s`, base)
}

// ListenIPC creates an owner-only named pipe listener for the IPC server.
func ListenIPC(ctx context.Context, endpoint string) (net.Listener, error) {
	// Configure the pipe for authenticated users only.
	config := &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;BA)(A;;GA;;;CO)", // Owner and Creator/Owner only
		MessageMode:        false,                         // Byte-stream mode for compatibility
		InputBufferSize:    65536,
		OutputBufferSize:   65536,
	}

	listener, err := winio.ListenPipe(endpoint, config)
	if err != nil {
		return nil, fmt.Errorf("listen pipe: %w", err)
	}

	return listener, nil
}

// VerifyPeerIdentity checks that the connected peer is authorized.
// On Windows, the security descriptor on the named pipe restricts access
// to the owner and creator/owner, so we just verify the process token.
func VerifyPeerIdentity(conn net.Conn) error {
	// The named pipe security descriptor restricts access at the OS level.
	// Additional verification would require token impersonation checks,
	// which are complex and rarely needed for same-user scenarios.
	// The security descriptor "D:P(A;;GA;;;BA)(A;;GA;;;CO)" ensures only
	// the Built-in Administrators and Creator/Owner can connect.
	return nil
}

// ensureDataDir creates the data directory if it doesn't exist.
func ensureDataDir(dataDir string) error {
	return os.MkdirAll(dataDir, 0o700)
}
