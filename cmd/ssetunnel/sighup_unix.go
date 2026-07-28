//go:build !windows

package main

import "syscall"

// syscallSIGHUP returns syscall.SIGHUP (Unix platforms).
func syscallSIGHUP() syscall.Signal { return syscall.SIGHUP }
