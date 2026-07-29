//go:build !windows

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

// syscallSIGHUP returns syscall.SIGHUP (Unix platforms).
func syscallSIGHUP() syscall.Signal { return syscall.SIGHUP }

// installSIGHUPHandler registers a SIGHUP listener that logs the given
// message.  The actual reload logic is a TODO per subcommand.
func installSIGHUPHandler(role, message string) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscallSIGHUP())
	go func() {
		for range hup {
			log.Printf("%s: %s", role, message)
		}
	}()
}
