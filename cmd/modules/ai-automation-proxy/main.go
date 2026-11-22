// Package main implements the AI automation proxy module — a standalone
// ssetunnel agent module that bridges external client requests to the
// gemini-automation-extension WXT browser extension via HTTP long polling.
//
// The module runs two HTTP surfaces:
//   - SDK invocation handler: client API for job submission and polling
//     (reachable through the ssetunnel tunnel)
//   - Extension HTTP server: long-polling endpoints for the WXT extension
//     (reachable on a configurable local port, default :9090)
//
// The agent's supervisor starts this binary as a child process and provides
// the IPC endpoint and credential via environment variables.
//
// The binary also answers sdk.ManifestFlag: the agent runs it that way to
// learn the module identity when it scans the installed module directory.
package main

import (
	"context"
	_ "embed"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wseternal/ssetunnel/internal/aiproxy"
	"github.com/wseternal/ssetunnel/sdk"
)

//go:embed openapi.yaml
var openapiYAML []byte

func main() {
	m := sdk.Manifest{
		ModuleID:       "com.wseternal.ai-automation-proxy",
		ServiceName:    "ai-automation-proxy",
		ServiceVersion: "0.1.0",
		OpenAPIYAML:    openapiYAML,
	}
	if sdk.PrintManifest(m) {
		return
	}

	log.SetFlags(log.Ltime | log.Lshortfile)

	listenAddr := flag.String("listen", "127.0.0.1:9090", "extension long-poll server listen address")
	jobTimeout := flag.Duration("job-timeout", 5*time.Minute, "default job timeout")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Job history is persisted through the tunnel-backed data store. The agent
	// provides the IPC endpoint and credential; without them (older agent, or a
	// manual run) the module keeps jobs in memory and still serves requests.
	var dataStore *sdk.DataStore
	if ds, err := sdk.NewDataStore(); err != nil {
		log.Printf("aiproxy: data store unavailable, job history is memory-only: %v", err)
	} else {
		dataStore = ds
		defer dataStore.Close()
		log.Printf("aiproxy: job history persisted via data store")
	}

	// Create job manager shared by both HTTP surfaces.
	jobs := aiproxy.NewJobManager(*jobTimeout, dataStore, m.ModuleID)
	defer jobs.Close()

	// Start the extension long-polling server on a separate port.
	extServer := aiproxy.NewExtensionServer(jobs)
	go func() {
		if err := extServer.ListenAndServe(*listenAddr); err != nil && err != http.ErrServerClosed {
			log.Fatalf("aiproxy: extension server: %v", err)
		}
	}()

	// Create the client API handler (served through SDK invocation).
	clientHandler := aiproxy.NewClientHandler(jobs)

	log.Printf("aiproxy: starting module %s (extension server on %s)", m.ModuleID, *listenAddr)

	if err := sdk.Serve(ctx, m, clientHandler); err != nil {
		log.Fatalf("aiproxy: %v", err)
	}

	// Graceful shutdown of extension server.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	extServer.Shutdown(shutdownCtx)
}
