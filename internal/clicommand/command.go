// Package clicommand implements the client-side command workflows shared
// by the cmd/cli and cmd/ssetunnel entry points. It is deliberately separate
// from internal/command so that cmd/server can import only server-side code
// without transitively pulling in agent, connect, and other client packages.
package clicommand

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/wseternal/ssetunnel/internal/clientapp"
	"github.com/wseternal/ssetunnel/internal/command"
)

// RunAgent starts the tunnel agent that dials out to the server and
// proxies streams to a local TCP target or spawns a local shell.
func RunAgent(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	serverURL := fs.String("server", "", "tunnel server URL (default: from saved session)")
	target := fs.String("target", "", "TCP address to forward streams to (empty = dynamic target mode)")
	agentID := fs.String("id", "", "agent identifier for routing (e.g. mydevbox)")
	batchSize := fs.Int("batch-size", 262144, "upstream batch ceiling in bytes (1024..1048576)")
	concurrency := fs.Int("concurrency", 1, "upstream POST sender depth (1..4)")
	compress := fs.Bool("compress", false, "negotiate gzip-per-batch upstream encoding")
	noAutoTune := fs.Bool("no-auto-tune", false, "disable server auto-tuning (keep static CLI flags)")
	verbose := fs.Bool("verbose", false, "enable detailed shell/control tracing logs")
	base := command.AcceptDeprecatedBaseFlag(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}
	command.WarnDeprecatedBase("agent", *base)

	log.Printf("agent: starting (server=%q, id=%q, target=%q)", *serverURL, *agentID, *target)

	// SIGHUP: trigger configuration reload (placeholder; agent has no
	// runtime-reloadable config today — logs only).
	command.InstallSIGHUPHandler("agent", "SIGHUP received (no reloadable config)")

	dataDir, dirErr := AgentDataDir()
	if dirErr != nil {
		return dirErr
	}

	return clientapp.RunAgent(ctx, clientapp.AgentConfig{
		ServerURL:   *serverURL,
		Target:      *target,
		AgentID:     *agentID,
		BatchSize:   *batchSize,
		Concurrency: *concurrency,
		Compress:    *compress,
		NoAutoTune:  *noAutoTune,
		Verbose:     *verbose,
		Version:     command.BuildVersion(),
		DataDir:     dataDir,
	})
}

// RunConnect starts the user-side connect client that wraps a connection
// to a remote agent via the tunnel server.
func RunConnect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	server := fs.String("server", "", "tunnel server URL (default: from saved session)")
	agentID := fs.String("agent", "", "agent identifier to connect to (e.g. mydevbox)")
	target := fs.String("target", "", "target address on the agent machine (e.g. 127.0.0.1:22)")
	local := fs.String("local", "", "local listen TCP address (e.g. 127.0.0.1:3306) or '-' for Stdio mode")
	batchSize := fs.Int("batch-size", 0, "upstream batch ceiling in bytes (0 = 256 KiB default; 1024..1048576)")
	base := command.AcceptDeprecatedBaseFlag(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}
	command.WarnDeprecatedBase("connect", *base)

	log.Printf("connect: starting (server=%q, agent=%q, target=%q, local=%q)", *server, *agentID, *target, *local)

	return clientapp.RunConnect(ctx, clientapp.ConnectConfig{
		ServerURL: *server,
		AgentID:   *agentID,
		Target:    *target,
		Local:     *local,
		BatchSize: *batchSize,
	})
}

// AgentDataDir returns the default agent data directory.
func AgentDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	return filepath.Join(home, ".ssetunnel", "agent"), nil
}
