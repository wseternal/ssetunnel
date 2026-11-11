package clientapp

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/wseternal/ssetunnel/internal/agent"
	"github.com/wseternal/ssetunnel/internal/auth"
)

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
		dataDir = filepath.Join(home, ".ssetunnel", "agent")
	}
	if err := ag.InitModules(ctx, dataDir); err != nil {
		return fmt.Errorf("init modules: %w", err)
	}

	return ag.Run(ctx)
}
