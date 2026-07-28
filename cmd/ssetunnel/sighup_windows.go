//go:build windows

package main

import "syscall"

// syscallSIGHUP returns a zero signal on Windows where SIGHUP does not
// exist.  sendReload guards against this with a runtime.GOOS check.
func syscallSIGHUP() syscall.Signal { return 0 }

// installSIGHUPHandler is a no-op on Windows where SIGHUP is not available.
func installSIGHUPHandler(_, _ string) {}
