package clientapp

import "os"

// processAlive reports whether the process with the given PID exists.
// On Windows, FindProcess returns an error for non-existent PIDs;
// for existing ones, a zero-signal check is not meaningful so we
// treat FindProcess success as alive.
func processAlive(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}
