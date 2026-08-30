package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testSessionDir sets up a temporary session directory and returns a cleanup func.
func testSessionDir(t *testing.T) func() {
	t.Helper()
	tmpDir := t.TempDir()
	// Override HOME for sessionDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	return func() { os.Setenv("HOME", origHome) }
}

func TestSaveAndLoadSession(t *testing.T) {
	cleanup := testSessionDir(t)
	defer cleanup()

	// Save a session
	err := SaveSession("http://server1:8080", "token1", "user1", "admin", "", time.Time{})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// Load with matching server
	token, server, _, _, err := LoadSession("http://server1:8080")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if token != "token1" {
		t.Errorf("token = %q, want %q", token, "token1")
	}
	if server != "http://server1:8080" {
		t.Errorf("server = %q, want %q", server, "http://server1:8080")
	}
}

func TestLoadSession_NoMatch(t *testing.T) {
	cleanup := testSessionDir(t)
	defer cleanup()

	// Save a session
	err := SaveSession("http://server1:8080", "token1", "user1", "admin", "", time.Time{})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// Load with non-matching server
	token, server, _, _, err := LoadSession("http://other:8080")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if token != "" {
		t.Errorf("token = %q, want empty", token)
	}
	if server != "" {
		t.Errorf("server = %q, want empty", server)
	}
}

func TestLoadSession_Default(t *testing.T) {
	cleanup := testSessionDir(t)
	defer cleanup()

	// Save a session
	err := SaveSession("http://server1:8080", "token1", "user1", "admin", "", time.Time{})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// Load with empty server (should return first entry)
	token, server, _, _, err := LoadSession("")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if token != "token1" {
		t.Errorf("token = %q, want %q", token, "token1")
	}
	if server != "http://server1:8080" {
		t.Errorf("server = %q, want %q", server, "http://server1:8080")
	}
}

func TestSaveSession_MultipleServers(t *testing.T) {
	cleanup := testSessionDir(t)
	defer cleanup()

	// Save sessions for two servers
	if err := SaveSession("http://server1:8080", "token1", "user1", "admin", "", time.Time{}); err != nil {
		t.Fatalf("SaveSession server1: %v", err)
	}
	if err := SaveSession("https://server2.com", "token2", "user2", "viewer", "", time.Time{}); err != nil {
		t.Fatalf("SaveSession server2: %v", err)
	}

	// Load each
	token1, srv1, _, _, _ := LoadSession("http://server1:8080")
	if token1 != "token1" || srv1 != "http://server1:8080" {
		t.Errorf("server1: token=%q server=%q", token1, srv1)
	}

	token2, srv2, _, _, _ := LoadSession("https://server2.com")
	if token2 != "token2" || srv2 != "https://server2.com" {
		t.Errorf("server2: token=%q server=%q", token2, srv2)
	}

	// List servers
	servers, err := SessionServers()
	if err != nil {
		t.Fatalf("SessionServers: %v", err)
	}
	if len(servers) != 2 {
		t.Errorf("SessionServers: got %d, want 2", len(servers))
	}
}

func TestLoadSession_NoFile(t *testing.T) {
	cleanup := testSessionDir(t)
	defer cleanup()

	// Load without any session file
	token, server, _, _, err := LoadSession("")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if token != "" || server != "" {
		t.Errorf("expected empty, got token=%q server=%q", token, server)
	}
}

func TestLoadSession_LegacyFormat(t *testing.T) {
	cleanup := testSessionDir(t)
	defer cleanup()

	// Write legacy format (plain token)
	dir, _ := sessionDir()
	path := filepath.Join(dir, "session")
	os.WriteFile(path, []byte("legacy-token-123"), 0600)

	// Load should treat legacy format as empty (no server match)
	token, server, _, _, err := LoadSession("http://server:8080")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	// Legacy format doesn't match any server
	if token != "" || server != "" {
		t.Errorf("legacy format should not match: token=%q server=%q", token, server)
	}
}

func TestSaveSession_Overwrite(t *testing.T) {
	cleanup := testSessionDir(t)
	defer cleanup()

	// Save initial
	if err := SaveSession("http://server:8080", "token1", "user1", "admin", "", time.Time{}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// Overwrite same server
	if err := SaveSession("http://server:8080", "token2", "user1", "admin", "", time.Time{}); err != nil {
		t.Fatalf("SaveSession overwrite: %v", err)
	}

	token, _, _, _, _ := LoadSession("http://server:8080")
	if token != "token2" {
		t.Errorf("token = %q, want %q after overwrite", token, "token2")
	}
}

func TestSaveSession_ExpiresAt(t *testing.T) {
	cleanup := testSessionDir(t)
	defer cleanup()

	exp := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	if err := SaveSession("http://server:8080", "tok", "user", "admin", "http://console:8081", exp); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	token, server, consoleURL, expiresAt, err := LoadSession("http://server:8080")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if token != "tok" {
		t.Errorf("token = %q, want %q", token, "tok")
	}
	if server != "http://server:8080" {
		t.Errorf("server = %q", server)
	}
	if consoleURL != "http://console:8081" {
		t.Errorf("consoleURL = %q, want %q", consoleURL, "http://console:8081")
	}
	if !expiresAt.Equal(exp) {
		t.Errorf("expiresAt = %v, want %v", expiresAt, exp)
	}
}

func TestLoadSession_BackwardCompat_NoExpiresAt(t *testing.T) {
	cleanup := testSessionDir(t)
	defer cleanup()

	// Save without expires_at (old session)
	if err := SaveSession("http://server:8080", "tok", "user", "admin", "", time.Time{}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	_, _, _, expiresAt, err := LoadSession("http://server:8080")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !expiresAt.IsZero() {
		t.Errorf("expiresAt = %v, want zero time for old session", expiresAt)
	}
}
