package clientapp

import (
	"syscall"
	"time"
)

// processAlive reports whether the process with the given PID exists.
// On Windows, os.FindProcess always succeeds, so we use OpenProcess +
// GetExitCodeProcess to actually verify the process is running.
func processAlive(pid int) bool {
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var exitCode uint32
	if err := syscall.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}
	return exitCode == 259 // STILL_ACTIVE
}

// terminateProcess sends a termination signal to the given PID and waits
// for it to exit. Returns nil if the process exited within the timeout.
func terminateProcess(pid int, timeout time.Duration) error {
	h, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE|syscall.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	if err := syscall.TerminateProcess(h, 1); err != nil {
		return err
	}
	// Poll for exit with timeout.
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var exitCode uint32
		if err := syscall.GetExitCodeProcess(h, &exitCode); err != nil {
			return nil // can't query — assume exited
		}
		if exitCode != 259 {
			return nil // exited
		}
		time.Sleep(100 * time.Millisecond)
	}
	return syscall.Errno(0) // timed out — caller decides how to handle
}

// TerminateExternalAgent terminates an agent process running outside
// the current process. It calls TerminateProcess and waits for exit.
// Returns nil on success, or an error if the process could not be terminated.
func TerminateExternalAgent(pid int, timeout time.Duration) error {
	return terminateProcess(pid, timeout)
}
