//go:build !windows

package clientapp

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/wseternal/ssetunnel/internal/agent"
)

// installReloadSignal registers a SIGHUP handler that reloads module
// state from disk and reconciles the supervisor. This allows CLI module
// commands (start, stop, install, etc.) running in a separate process
// to notify the running agent via SIGHUP.
func installReloadSignal(ag *agent.Agent) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			log.Printf("agent: SIGHUP received, reloading modules...")
			if err := ag.ReloadModules(); err != nil {
				log.Printf("agent: module reload: %v", err)
			}
		}
	}()
}
