//go:build !windows

package clientapp

import (
	"os"
	"syscall"
	"time"
)

// processAlive reports whether the process with the given PID exists
// and can be signalled. On Unix, sending signal 0 checks existence
// without affecting the target process.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// terminateProcess sends SIGTERM to the given PID and waits for it to exit.
// Returns nil if the process exited within the timeout, or an error if
// the process could not be signalled or did not exit in time.
func terminateProcess(pid int, timeout time.Duration) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	// Poll for exit with timeout.
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return nil // process exited
		}
		time.Sleep(100 * time.Millisecond)
	}
	return syscall.ETIMEDOUT
}

// TerminateExternalAgent terminates an agent process running outside
// the current process. It sends SIGTERM and waits for the process to exit.
// Returns nil on success, or an error if the process could not be terminated.
func TerminateExternalAgent(pid int, timeout time.Duration) error {
	return terminateProcess(pid, timeout)
}
