package auth

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// SessionEntry holds the credentials for a single server.
type SessionEntry struct {
	Token      string `json:"token"`
	Username   string `json:"username,omitempty"`
	Role       string `json:"role,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`  // RFC 3339
	ConsoleURL string `json:"console_url,omitempty"` // console API base URL for token refresh
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
	return sf, nil
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
// Multiple servers can be stored; each keyed by serverURL.
// consoleURL and expiresAt may be empty/zero for backward compatibility.
func SaveSession(serverURL, token, username, role, consoleURL string, expiresAt time.Time) error {
	sf, err := loadSessionFile()
	if err != nil {
		return fmt.Errorf("load session file: %w", err)
	}
	entry := SessionEntry{
		Token:      token,
		Username:   username,
		Role:       role,
		ConsoleURL: consoleURL,
	}
	if !expiresAt.IsZero() {
		entry.ExpiresAt = expiresAt.Format(time.RFC3339)
	}
	sf.Sessions[serverURL] = entry
	return saveSessionFile(sf)
}

// LoadSession reads the session entry for the given server URL.
// If serverURL is empty, returns the first entry (for backward compat).
// Returns zero time for expiresAt when the entry lacks expiry metadata (old sessions).
func LoadSession(serverURL string) (token, resolvedServer, consoleURL string, expiresAt time.Time, err error) {
	sf, err := loadSessionFile()
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	lookup := func(entry SessionEntry, srv string) (string, string, string, time.Time) {
		var t time.Time
		if entry.ExpiresAt != "" {
			t, _ = time.Parse(time.RFC3339, entry.ExpiresAt)
		}
		return entry.Token, srv, entry.ConsoleURL, t
	}
	if serverURL != "" {
		if entry, ok := sf.Sessions[serverURL]; ok {
			tok, srv, curl, exp := lookup(entry, serverURL)
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

// SessionServers returns the list of server URLs with stored sessions.
func SessionServers() ([]string, error) {
	sf, err := loadSessionFile()
	if err != nil {
		return nil, err
	}
	servers := make([]string, 0, len(sf.Sessions))
	for srv := range sf.Sessions {
		servers = append(servers, srv)
	}
	sort.Strings(servers)
	return servers, nil
}
