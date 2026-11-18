//go:build windows

package clientapp

import "github.com/wseternal/ssetunnel/internal/agent"

// installReloadSignal is a no-op on Windows where SIGHUP is not available.
// Module state changes require an agent restart on Windows.
func installReloadSignal(_ *agent.Agent) {}
