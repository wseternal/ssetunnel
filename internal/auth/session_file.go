package auth

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SessionEntry holds the credentials for a single server.
type SessionEntry struct {
	Token      string `json:"token"`
	Username   string `json:"username,omitempty"`
	Role       string `json:"role,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`  // RFC 3339
	ConsoleURL string `json:"console_url,omitempty"` // console API base URL for token refresh
	ServerURL  string `json:"server_url,omitempty"`  // full tunnel server URL (preserved for agents)
}

// sessionFile is the on-disk format for ~/.ssetunnel/session.
type sessionFile struct {
	Sessions map[string]SessionEntry `json:"sessions"`
}

// sessionDir returns ~/.ssetunnel/, creating it if needed.
func sessionDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	dir := filepath.Join(home, ".ssetunnel")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create session dir: %w", err)
	}
	return dir, nil
}

// SessionFilePath returns the path to the session file.
func SessionFilePath() (string, error) {
	dir, err := sessionDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "session"), nil
}

// loadSessionFile reads and parses the session file.
// Returns an empty sessionFile if the file does not exist.
func loadSessionFile() (sessionFile, error) {
	path, err := SessionFilePath()
	if err != nil {
		return sessionFile{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sessionFile{Sessions: make(map[string]SessionEntry)}, nil
		}
		return sessionFile{}, fmt.Errorf("read session file: %w", err)
	}
	var sf sessionFile
	if err := json.Unmarshal(data, &sf); err != nil {
		// Legacy format (plain token string) or corrupted JSON — start fresh.
		// The old token cannot be migrated because it lacks server URL, username,
		// and role metadata. Users must re-run `ssetunnel login`.
		log.Printf("auth: WARNING: session file uses legacy or corrupted format; all old sessions discarded — re-run `ssetunnel login`")
		return sessionFile{Sessions: make(map[string]SessionEntry)}, nil
	}
	if sf.Sessions == nil {
		sf.Sessions = make(map[string]SessionEntry)
	}
	// Migrate legacy full-URL keys to hostname keys. Pre-hostname-normalization
	// session files store entries keyed by the full server URL (e.g.
	// "http://server:8080"). Re-key them on load so upgraded users don't
	// silently lose their sessions.
	if migrated := migrateSessionKeys(&sf); migrated {
		_ = saveSessionFile(sf)
		log.Printf("auth: migrated session keys from URL format to hostname format")
	}
	return sf, nil
}

// migrateSessionKeys re-keys any entry whose key differs from sessionKey(key)
// (i.e. legacy full-URL keys). Populates ServerURL from the old key when empty.
// Returns true if any migration occurred.
func migrateSessionKeys(sf *sessionFile) bool {
	migrated := false
	for key, entry := range sf.Sessions {
		hk := sessionKey(key)
		if hk == key {
			continue
		}
		if entry.ServerURL == "" {
			entry.ServerURL = key
		}
		// Only migrate if the target key doesn't already exist (avoid clobbering).
		if _, exists := sf.Sessions[hk]; !exists {
			sf.Sessions[hk] = entry
		}
		delete(sf.Sessions, key)
		migrated = true
	}
	return migrated
}

// sessionKey extracts the hostname from a URL for session map keying.
// This ensures that the same server is recognized regardless of the path,
// port, or scheme used to reach it (e.g. /console vs /sse behind a proxy).
// Falls back to the raw URL if parsing fails.
func sessionKey(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	// Handle bare host:port (no scheme) — url.Parse treats the first
	// segment as the scheme, yielding an empty hostname.
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return rawURL
	}
	return u.Hostname()
}

// saveSessionFile writes the session file.
func saveSessionFile(sf sessionFile) error {
	path, err := SessionFilePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

// SaveSession writes the session entry for the given server URL.
// The session is keyed by hostname (via sessionKey) so that the same
// credential is found regardless of path, port, or scheme.
// consoleURL and expiresAt may be empty/zero for backward compatibility.
func SaveSession(serverURL, token, username, role, consoleURL string, expiresAt time.Time) error {
	sf, err := loadSessionFile()
	if err != nil {
		return fmt.Errorf("load session file: %w", err)
	}
	// Strip userinfo (passwords) from URLs before persisting.
	serverURL = stripURLUserinfo(serverURL)
	consoleURL = stripURLUserinfo(consoleURL)
	entry := SessionEntry{
		Token:      token,
		Username:   username,
		Role:       role,
		ConsoleURL: consoleURL,
		ServerURL:  serverURL,
	}
	if !expiresAt.IsZero() {
		entry.ExpiresAt = expiresAt.Format(time.RFC3339)
	}
	sf.Sessions[sessionKey(serverURL)] = entry
	return saveSessionFile(sf)
}

// LoadSession reads the session entry for the given server URL.
// The lookup is keyed by hostname (via sessionKey).
// If serverURL is empty, returns the first entry (for backward compat).
// Returns zero time for expiresAt when the entry lacks expiry metadata (old sessions).
func LoadSession(serverURL string) (token, resolvedServer, consoleURL string, expiresAt time.Time, err error) {
	sf, err := loadSessionFile()
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	lookup := func(entry SessionEntry, key string) (string, string, string, time.Time) {
		var t time.Time
		if entry.ExpiresAt != "" {
			var parseErr error
			t, parseErr = time.Parse(time.RFC3339, entry.ExpiresAt)
			if parseErr != nil {
				log.Printf("auth: WARNING: corrupted expires_at %q in session file, treating as unknown expiry", entry.ExpiresAt)
			}
		}
		// Prefer the stored full URL; fall back to the map key for
		// legacy sessions saved before ServerURL was added.
		resolved := entry.ServerURL
		if resolved == "" {
			resolved = key
		}
		return entry.Token, resolved, entry.ConsoleURL, t
	}
	if serverURL != "" {
		key := sessionKey(serverURL)
		if entry, ok := sf.Sessions[key]; ok {
			tok, srv, curl, exp := lookup(entry, key)
			return tok, srv, curl, exp, nil
		}
		return "", "", "", time.Time{}, nil
	}
	// No server specified — return first entry (sorted for determinism).
	keys := make([]string, 0, len(sf.Sessions))
	for srv := range sf.Sessions {
		keys = append(keys, srv)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		entry := sf.Sessions[keys[0]]
		tok, srv, curl, exp := lookup(entry, keys[0])
		return tok, srv, curl, exp, nil
	}
	return "", "", "", time.Time{}, nil
}

// UpdateSessionToken updates only the token and expiry for an existing session
// entry, preserving all other fields (username, role, consoleURL, serverURL).
// This avoids the data-loss that occurs when SaveSession overwrites the entire entry.
func UpdateSessionToken(serverURL, newToken string, expiresAt time.Time) error {
	sf, err := loadSessionFile()
	if err != nil {
		return fmt.Errorf("load session file: %w", err)
	}
	key := sessionKey(serverURL)
	entry, ok := sf.Sessions[key]
	if !ok {
		return fmt.Errorf("no session for %q (key=%q)", serverURL, key)
	}
	entry.Token = newToken
	if !expiresAt.IsZero() {
		entry.ExpiresAt = expiresAt.Format(time.RFC3339)
	}
	// Keep ServerURL current if the caller provides a different URL,
	// so subsequent agents get the latest tunnel endpoint.
	if serverURL != entry.ServerURL {
		entry.ServerURL = serverURL
	}
	sf.Sessions[key] = entry
	return saveSessionFile(sf)
}

// LoadSessionEntry reads the full session entry for the given server URL.
// Returns nil when no matching entry exists.
func LoadSessionEntry(serverURL string) (*SessionEntry, error) {
	sf, err := loadSessionFile()
	if err != nil {
		return nil, err
	}
	if serverURL != "" {
		key := sessionKey(serverURL)
		if entry, ok := sf.Sessions[key]; ok {
			return &entry, nil
		}
		return nil, nil
	}
	// No server specified — return first entry (sorted for determinism).
	keys := make([]string, 0, len(sf.Sessions))
	for srv := range sf.Sessions {
		keys = append(keys, srv)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		entry := sf.Sessions[keys[0]]
		return &entry, nil
	}
	return nil, nil
}

// DeleteSession removes the session entry for the given server URL.
// If serverURL is empty, deletes all entries (full logout).
func DeleteSession(serverURL string) error {
	sf, err := loadSessionFile()
	if err != nil {
		return err
	}
	if serverURL != "" {
		delete(sf.Sessions, sessionKey(serverURL))
	} else {
		sf.Sessions = make(map[string]SessionEntry)
	}
	return saveSessionFile(sf)
}

// ClearSessionToken clears the token and expiry for the session entry
// matching the given server URL, but preserves the server metadata
// (server URL, console URL, username) so the entry remains available
// for server selection UIs. Returns nil when no matching entry exists.
func ClearSessionToken(serverURL string) error {
	sf, err := loadSessionFile()
	if err != nil {
		return err
	}
	key := sessionKey(serverURL)
	entry, ok := sf.Sessions[key]
	if !ok {
		return nil
	}
	entry.Token = ""
	entry.ExpiresAt = ""
	sf.Sessions[key] = entry
	return saveSessionFile(sf)
}

// SessionServers returns the list of server URLs with stored sessions.
func SessionServers() ([]string, error) {
	sf, err := loadSessionFile()
	if err != nil {
		return nil, err
	}
	servers := make([]string, 0, len(sf.Sessions))
	for key, entry := range sf.Sessions {
		srv := entry.ServerURL
		if srv == "" {
			srv = key // legacy fallback: key is hostname only
		}
		servers = append(servers, srv)
	}
	sort.Strings(servers)
	return servers, nil
}

// ListAllSessions returns all session entries from the session file.
// Each entry's ServerURL is populated, falling back to the map key for
// legacy sessions.
func ListAllSessions() ([]SessionEntry, error) {
	sf, err := loadSessionFile()
	if err != nil {
		return nil, err
	}
	entries := make([]SessionEntry, 0, len(sf.Sessions))
	for key, entry := range sf.Sessions {
		if entry.ServerURL == "" {
			entry.ServerURL = key
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ServerURL < entries[j].ServerURL
	})
	return entries, nil
}

// stripURLUserinfo removes userinfo (user:password) from a URL to prevent
// plaintext credentials from being persisted to the session file.
func stripURLUserinfo(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil {
		return rawURL
	}
	u.User = nil
	return u.String()
}
