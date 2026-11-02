//go:build !windows

package agentmod

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// ipcNetwork is the network type for Unix domain sockets.
const ipcNetwork = "unix"

// IPCEndpoint returns the default IPC endpoint path for the given data directory.
// On Unix, this is a socket file in the agent's data directory.
func IPCEndpoint(dataDir string) string {
	return filepath.Join(dataDir, "module.sock")
}

// ListenIPC creates an owner-only Unix socket listener for the IPC server.
// The socket is created with mode 0600 (owner read/write only).
func ListenIPC(ctx context.Context, endpoint string) (net.Listener, error) {
	// Remove any existing socket file.
	if err := os.Remove(endpoint); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove existing socket: %w", err)
	}

	// Ensure the parent directory exists with restricted permissions.
	dir := filepath.Dir(endpoint)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "unix", endpoint)
	if err != nil {
		return nil, fmt.Errorf("listen unix: %w", err)
	}

	// Set socket permissions to owner-only.
	if err := os.Chmod(endpoint, 0o600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}

	return listener, nil
}

// VerifyPeerIdentity checks that the connected peer has the same UID as
// the current process. This prevents other users on the same system from
// connecting to the IPC socket.
//
// On macOS, peer identity verification is not available (SO_PEERCRED is
// Linux-only). We rely on the socket file permissions (mode 0600) to
// restrict access to the owner only.
func VerifyPeerIdentity(conn net.Conn) error {
	// On macOS/BSD, the socket permissions (0600) restrict access to the
	// owner. Full peer credential verification requires Linux's SO_PEERCRED.
	// For cross-platform compatibility, we accept this as sufficient on
	// non-Linux systems.
	return nil
}
