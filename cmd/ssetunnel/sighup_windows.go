//go:build windows

package main

import "syscall"

// syscallSIGHUP returns a zero signal on Windows where SIGHUP does not
// exist.  sendReload guards against this with a runtime.GOOS check, and
// installSIGHUPHandler registering signal 0 is harmless (never fires).
func syscallSIGHUP() syscall.Signal { return 0 }
