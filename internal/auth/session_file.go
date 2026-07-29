package auth

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
)

// SessionEntry holds the credentials for a single server.
type SessionEntry struct {
	Token    string `json:"token"`
	Username string `json:"username,omitempty"`
	Role     string `json:"role,omitempty"`
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
		// Legacy format (plain token) or corrupted — start fresh.
		log.Printf("auth: WARNING: session file is in legacy or corrupted format; starting fresh (old data discarded)")
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
func SaveSession(serverURL, token, username, role string) error {
	sf, err := loadSessionFile()
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("load session file: %w", err)
		}
		sf = sessionFile{Sessions: make(map[string]SessionEntry)}
	}
	sf.Sessions[serverURL] = SessionEntry{
		Token:    token,
		Username: username,
		Role:     role,
	}
	return saveSessionFile(sf)
}

// LoadSession reads the session entry for the given server URL.
// If serverURL is empty, returns the first entry (for backward compat).
// Returns ("", "", nil) if no matching session exists.
func LoadSession(serverURL string) (token, resolvedServer string, err error) {
	sf, err := loadSessionFile()
	if err != nil {
		return "", "", err
	}
	if serverURL != "" {
		if entry, ok := sf.Sessions[serverURL]; ok {
			return entry.Token, serverURL, nil
		}
		return "", "", nil
	}
	// No server specified — return first entry (sorted for determinism).
	keys := make([]string, 0, len(sf.Sessions))
	for srv := range sf.Sessions {
		keys = append(keys, srv)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		entry := sf.Sessions[keys[0]]
		return entry.Token, keys[0], nil
	}
	return "", "", nil
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
