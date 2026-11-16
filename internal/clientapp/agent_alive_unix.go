//go:build !windows

package clientapp

import (
	"os"
	"syscall"
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
