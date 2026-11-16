package clientapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/wseternal/ssetunnel/internal/agent"
	"github.com/wseternal/ssetunnel/internal/auth"
)

// DefaultDataDir is the agent data directory used when AgentConfig.DataDir
// is empty. Both the CLI and the browser server reference this constant so
// the module storage path stays consistent across surfaces.
var DefaultDataDir = filepath.Join(".ssetunnel", "agent")

// logOutputMu serializes swaps of the global log writer so concurrent
// agent runs and the UI server never race on log.SetOutput/log.Writer.
var logOutputMu sync.Mutex

// swapLogOutput atomically swaps the global log writer under a mutex and
// returns the previous writer. Callers must restore it with a deferred swap.
// log.SetOutput and log.Writer are not goroutine-safe on their own.
func swapLogOutput(w io.Writer) io.Writer {
	logOutputMu.Lock()
	defer logOutputMu.Unlock()
	prev := log.Writer()
	log.SetOutput(w)
	return prev
}

// AgentConfig contains the parameters for starting an agent.
type AgentConfig struct {
	// ServerURL overrides the saved session URL when set.
	ServerURL string
	// Target TCP address to forward streams to (empty = dynamic target mode).
	Target string
	// AgentID for routing (e.g. mydevbox).
	AgentID string
	// BatchSize upstream batch ceiling in bytes (1024..1048576).
	BatchSize int
	// Concurrency upstream POST sender depth (1..4).
	Concurrency int
	// Compress negotiates gzip-per-batch upstream encoding.
	Compress bool
	// NoAutoTune disables server auto-tuning.
	NoAutoTune bool
	// Verbose enables detailed shell/control tracing logs.
	Verbose bool
	// Version is the build version string for logging.
	Version string
	// DataDir is the agent data directory for module storage.
	DataDir string
}

// RunAgent starts the agent with the given configuration and blocks until
// the context is cancelled or the agent exits.
func RunAgent(ctx context.Context, cfg AgentConfig) error {
	batch, conc := ClampAgentFlags(cfg.BatchSize, cfg.Concurrency)

	sess, err := ResolveSession(cfg.ServerURL, "agent")
	if err != nil {
		return err
	}

	// Build request modifier for session-based auth.
	// A mutex guards the token string so the refresh callback and
	// the agent's HTTP sender goroutines never read/write it concurrently.
	var reqMod func(*http.Request)
	var tokenMu sync.RWMutex
	tokenVal := sess.Token
	if sess.Token != "" {
		reqMod = func(req *http.Request) {
			tokenMu.RLock()
			t := tokenVal
			tokenMu.RUnlock()
			req.Header.Set("Authorization", "Bearer "+t)
		}
	}

	// Build mid-lifecycle token refresh callback.
	var refreshFn func() (string, error)
	if sess.Token != "" && sess.ConsoleURL != "" {
		serverKey := sess.URL
		cURL := sess.ConsoleURL
		refreshFn = func() (string, error) {
			_, _, _, expiresAt, loadErr := auth.LoadSession(serverKey)
			if loadErr != nil {
				log.Printf("agent: warning: cannot read session file: %v", loadErr)
				tokenMu.RLock()
				t := tokenVal
				tokenMu.RUnlock()
				return t, nil
			}
			if !auth.NeedsRefresh(expiresAt) {
				tokenMu.RLock()
				t := tokenVal
				tokenMu.RUnlock()
				return t, nil
			}
			tokenMu.RLock()
			currentTok := tokenVal
			tokenMu.RUnlock()
			newTok, newExp, err := auth.RefreshSession(cURL, currentTok)
			if err != nil {
				return "", err
			}
			if saveErr := auth.UpdateSessionToken(serverKey, newTok, newExp); saveErr != nil {
				log.Printf("agent: warning: failed to save refreshed session: %v", saveErr)
			}
			tokenMu.Lock()
			tokenVal = newTok
			tokenMu.Unlock()
			return newTok, nil
		}
	}

	if cfg.Version != "" {
		log.Printf("agent: ssetunnel %s", cfg.Version)
	}
	if cfg.Target != "" {
		log.Printf("agent: target %s -> server %s (id=%s, batch-size %d, concurrency %d, compress %v)",
			cfg.Target, sess.URL, cfg.AgentID, batch, conc, cfg.Compress)
	} else {
		log.Printf("agent: dynamic target mode -> server %s (id=%s, batch-size %d, concurrency %d, compress %v)",
			sess.URL, cfg.AgentID, batch, conc, cfg.Compress)
	}

	// Write a status file so the browser UI can detect this agent
	// even when it was started outside the UI process (e.g. `cli agent run`).
	// Service-managed agents also write a PID file via serviceProgram.Start;
	// this status file carries the same PID plus config for the UI to display.
	statusPath := agentStatusFilePath()
	writeAgentStatusFile(statusPath, AgentStatusInfo{
		PID:       os.Getpid(),
		ServerURL: sess.URL,
		Target:    cfg.Target,
		AgentID:   cfg.AgentID,
	})
	defer removeAgentStatusFile(statusPath)

	ag := &agent.Agent{
		ServerURL:       sess.URL,
		Target:          cfg.Target,
		AgentID:         cfg.AgentID,
		Token:           sess.Token,
		RequestModifier: reqMod,
		BatchSize:       batch,
		Concurrency:     conc,
		Compress:        cfg.Compress,
		NoAutoTune:      cfg.NoAutoTune,
		Verbose:         cfg.Verbose,
		OnTokenRefresh:  refreshFn,
	}

	dataDir := cfg.DataDir
	if dataDir == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return fmt.Errorf("get home directory: %w", homeErr)
		}
		dataDir = filepath.Join(home, DefaultDataDir)
	}
	if err := ag.InitModules(ctx, dataDir); err != nil {
		return fmt.Errorf("init modules: %w", err)
	}
	defer ag.Close()

	// Install log capture under the mutex so ag.Run skips its own
	// unprotected log.SetOutput (see Agent.InitLogCapture).
	// The MultiWriter tees all log.Printf output to stderr and the
	// agent's ring buffer; the deferred restore puts it back.
	buf := ag.InitLogCapture()
	prevLog := swapLogOutput(io.MultiWriter(os.Stderr, buf))
	defer swapLogOutput(prevLog)

	return ag.Run(ctx)
}

// AgentStatusInfo is the JSON payload written to the agent status file.
// The browser UI reads this to detect agents running outside its process.
type AgentStatusInfo struct {
	PID       int    `json:"pid"`
	ServerURL string `json:"server_url"`
	Target    string `json:"target"`
	AgentID   string `json:"agent_id"`
}

// agentStatusFilePath returns ~/.ssetunnel/ssetunnel-agent.json.
// This mirrors the service PID file location (~/.ssetunnel/ssetunnel-agent.pid)
// but carries additional config fields for the UI to display.
func agentStatusFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".ssetunnel", "ssetunnel-agent.json")
	}
	return filepath.Join(home, ".ssetunnel", "ssetunnel-agent.json")
}

func writeAgentStatusFile(path string, info AgentStatusInfo) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Printf("agent: status file: mkdir: %v", err)
		return
	}
	data, err := json.Marshal(info)
	if err != nil {
		log.Printf("agent: status file: marshal: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		log.Printf("agent: status file: write: %v", err)
	}
}

// removeAgentStatusFile deletes the status file only if it still contains
// the current process's PID, so a replacement agent's file is not removed.
func removeAgentStatusFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // already gone or unreadable — nothing to clean
	}
	var info AgentStatusInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return
	}
	if info.PID != os.Getpid() {
		return // another agent owns the file
	}
	_ = os.Remove(path)
}

// ReadExternalAgentStatus checks for an agent status file written by a
// process outside the UI server (e.g. `cli agent run` or a service).
// It returns nil when no file exists or the recorded PID is dead.
func ReadExternalAgentStatus() *AgentStatusInfo {
	path := agentStatusFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var info AgentStatusInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil
	}
	if info.PID <= 0 || !processAlive(info.PID) {
		return nil
	}
	return &info
}
