package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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

// SaveSession writes the session token to ~/.ssetunnel/session with mode 0600.
func SaveSession(token string) error {
	path, err := SessionFilePath()
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(token), 0600)
}

// LoadSession reads the session token from ~/.ssetunnel/session.
// Returns empty string if the file does not exist.
func LoadSession() (string, error) {
	path, err := SessionFilePath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read session file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}
