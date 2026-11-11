package clientapp

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
)

const (
	// MinBatchSize is the minimum upstream batch ceiling in bytes.
	MinBatchSize = 1024
	// MaxBatchSize is the maximum upstream batch ceiling in bytes.
	MaxBatchSize = 1 << 20
	// MinConcurrency is the minimum upstream POST sender depth.
	MinConcurrency = 1
	// MaxConcurrency is the maximum upstream POST sender depth.
	MaxConcurrency = 4
)

// SessionInfo contains resolved session metadata.
type SessionInfo struct {
	URL        string
	Token      string
	ConsoleURL string
	ExpiresAt  time.Time
}

// ResolveSession resolves the server URL and session token.
// If serverURL is set, it uses it and loads the matching token.
// If serverURL is empty, it loads the first saved session.
// Proactively refreshes the token if it is near expiration.
func ResolveSession(serverURL, prefix string) (*SessionInfo, error) {
	serverURL = strings.TrimRight(serverURL, "/")
	token, resolvedServer, consoleURL, expiresAt, sessErr := auth.LoadSession(serverURL)

	url := serverURL
	if url == "" {
		if sessErr != nil {
			return nil, fmt.Errorf("--server is required (failed to load saved session: %w); run login first", sessErr)
		}
		if resolvedServer != "" {
			url = resolvedServer
			log.Printf("%s: using saved session for %s", prefix, url)
		} else {
			return nil, fmt.Errorf("--server is required (no saved session found; run login first)")
		}
	} else {
		if sessErr != nil {
			log.Printf("%s: warning: failed to load session for %s: %v (proceeding without saved token)", prefix, url, sessErr)
		} else if token != "" {
			log.Printf("%s: using saved session for %s", prefix, url)
		}
	}

	// Warn about old-format sessions that lack refresh metadata.
	if token != "" && consoleURL == "" && expiresAt.IsZero() {
		log.Printf("%s: session lacks refresh metadata (old format); token may expire without warning — re-run login to upgrade", prefix)
	}

	// Proactive token refresh when approaching expiration.
	if token != "" && consoleURL != "" && auth.NeedsRefresh(expiresAt) {
		remaining := time.Until(expiresAt)
		if remaining < 0 {
			log.Printf("%s: session expired, attempting refresh...", prefix)
		} else {
			log.Printf("%s: session expires in %s, refreshing...", prefix, remaining.Round(time.Hour))
		}
		newToken, newExpiresAt, refreshErr := auth.RefreshSession(consoleURL, token)
		if refreshErr != nil {
			// Another process may have refreshed already — re-read from disk.
			if tok2, _, _, exp2, loadErr := auth.LoadSession(serverURL); loadErr == nil && tok2 != "" && tok2 != token && !auth.NeedsRefresh(exp2) {
				token = tok2
				log.Printf("%s: token was refreshed by another process", prefix)
			} else if remaining < 0 {
				return nil, fmt.Errorf("session expired and refresh failed: %w; run login to re-authenticate", refreshErr)
			} else {
				log.Printf("%s: warning: token refresh failed: %v (continuing with current token)", prefix, refreshErr)
			}
		} else {
			log.Printf("%s: session token refreshed (expires %s)", prefix, newExpiresAt.Format("2006-01-02"))
			if saveErr := auth.UpdateSessionToken(url, newToken, newExpiresAt); saveErr != nil {
				log.Printf("%s: warning: failed to save refreshed session: %v", prefix, saveErr)
			}
			token = newToken
			expiresAt = newExpiresAt
		}
	}

	return &SessionInfo{
		URL:        url,
		Token:      token,
		ConsoleURL: consoleURL,
		ExpiresAt:  expiresAt,
	}, nil
}

// ClampBatchSize bounds the batch size to the allowed range.
func ClampBatchSize(batchSize int) int {
	if batchSize < MinBatchSize {
		log.Printf("batch-size %d below minimum, clamped to %d", batchSize, MinBatchSize)
		return MinBatchSize
	}
	if batchSize > MaxBatchSize {
		log.Printf("batch-size %d above maximum, clamped to %d", batchSize, MaxBatchSize)
		return MaxBatchSize
	}
	return batchSize
}

// ClampConcurrency bounds the concurrency to the allowed range.
func ClampConcurrency(concurrency int) int {
	if concurrency < MinConcurrency {
		log.Printf("concurrency %d below minimum, clamped to %d", concurrency, MinConcurrency)
		return MinConcurrency
	}
	if concurrency > MaxConcurrency {
		log.Printf("concurrency %d above maximum, clamped to %d", concurrency, MaxConcurrency)
		return MaxConcurrency
	}
	return concurrency
}

// ClampAgentFlags bounds both agent flag values to their allowed ranges.
func ClampAgentFlags(batchSize, concurrency int) (int, int) {
	return ClampBatchSize(batchSize), ClampConcurrency(concurrency)
}
