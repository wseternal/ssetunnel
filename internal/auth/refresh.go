package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// refreshHTTPClient is a bounded-timeout client for refresh requests.
// Using http.DefaultClient would risk hanging the CLI indefinitely if the
// console server is unreachable.
var refreshHTTPClient = &http.Client{Timeout: 15 * time.Second}

// sessionRefreshThreshold is the remaining TTL below which the client
// proactively refreshes the session token.
const sessionRefreshThreshold = 7 * 24 * time.Hour

// NeedsRefresh reports whether a session token should be refreshed based on
// its remaining TTL. Returns false for zero expiry (old sessions without
// metadata) to maintain backward compatibility.
func NeedsRefresh(expiresAt time.Time) bool {
	if expiresAt.IsZero() {
		return false
	}
	return time.Until(expiresAt) < sessionRefreshThreshold
}

// RefreshSession calls the server's refresh-session endpoint and returns the
// new token and its expiry time. consoleURL is the base console server URL
// (e.g. "http://server:8081").
func RefreshSession(consoleURL, currentToken string) (newToken string, newExpiresAt time.Time, err error) {
	url := strings.TrimRight(consoleURL, "/") + "/console/api/v1/refresh-session"

	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+currentToken)

	resp, err := refreshHTTPClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", time.Time{}, fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", time.Time{}, fmt.Errorf("parse refresh response: %w", err)
	}

	if result.Token == "" {
		return "", time.Time{}, fmt.Errorf("refresh response missing token")
	}
	expiresAt, err := time.Parse(time.RFC3339, result.ExpiresAt)
	if err != nil {
		log.Printf("auth: warning: server returned unparseable expires_at %q", result.ExpiresAt)
		return result.Token, time.Time{}, nil // token valid but no expiry info
	}
	return result.Token, expiresAt, nil
}
