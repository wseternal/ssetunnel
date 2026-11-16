//go:build !windows

package clientapp

import (
	"os"
	"syscall"
	"time"
)

// processAlive reports whether the process with the given PID exists
// and can be signalled. On Unix, sending signal 0 checks existence
// without affecting the target process. EPERM means the process exists
// under a different UID (e.g. a service running as root), so we treat
// it as alive.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || err.Error() == "os: permission denied" || err.Error() == "os: operation not permitted"
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
		// ESRCH: process already exited between ReadExternalAgentStatus
		// and this signal — not an error, the goal (process gone) is met.
		msg := err.Error()
		if msg == "os: process already finished" || msg == "os: no such process" {
			return nil
		}
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
