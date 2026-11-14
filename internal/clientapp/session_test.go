package clientapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
)

func TestClampAgentFlags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		batchSize       int
		concurrency     int
		wantBatchSize   int
		wantConcurrency int
	}{
		{"defaults pass through", 16384, 1, 16384, 1},
		{"tuned values pass through", 65536, 4, 65536, 4},
		{"batch below min clamps up", 512, 1, 1024, 1},
		{"batch above max clamps down", 2 << 20, 1, 1 << 20, 1},
		{"concurrency below min clamps up", 16384, 0, 16384, 1},
		{"concurrency above max clamps down", 16384, 8, 16384, 4},
		{"both clamp", 1, 99, 1024, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotBatch, gotConc := ClampAgentFlags(tt.batchSize, tt.concurrency)
			if gotBatch != tt.wantBatchSize || gotConc != tt.wantConcurrency {
				t.Fatalf("ClampAgentFlags(%d, %d) = (%d, %d), want (%d, %d)",
					tt.batchSize, tt.concurrency, gotBatch, gotConc, tt.wantBatchSize, tt.wantConcurrency)
			}
		})
	}
}

func TestResolveSession_NoFlag_NoSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	_, err := ResolveSession("", "test")
	if err == nil {
		t.Fatal("expected error when no flag and no session, got nil")
	}
	if !strings.Contains(err.Error(), "--server is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveSession_NoFlag_WithSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := auth.SaveSession("http://saved:8080", "tok1", "user1", "admin", "", time.Time{}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	sess, err := ResolveSession("", "test")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if sess.URL != "http://saved:8080" {
		t.Errorf("url = %q, want %q", sess.URL, "http://saved:8080")
	}
	if sess.Token != "tok1" {
		t.Errorf("token = %q, want %q", sess.Token, "tok1")
	}
}

func TestResolveSession_RefreshSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	refreshCalled := false
	newToken := "refreshed-token-xyz"
	newExpiresAt := time.Now().UTC().Add(30 * 24 * time.Hour)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/refresh-session" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer old-token" {
			t.Errorf("auth = %q, want Bearer old-token", authHeader)
		}
		refreshCalled = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"token":      newToken,
			"expires_at": newExpiresAt.Format(time.RFC3339),
		})
	}))
	defer srv.Close()

	// Save session with near-expiry (3 days) and consoleURL pointing at the
	// refresh server.
	nearExpiry := time.Now().Add(3 * 24 * time.Hour)
	if err := auth.SaveSession("http://tunnel:8080", "old-token", "user1", "admin", srv.URL, nearExpiry); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	sess, err := ResolveSession("", "test")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if sess.URL != "http://tunnel:8080" {
		t.Errorf("url = %q, want %q", sess.URL, "http://tunnel:8080")
	}
	if sess.Token != newToken {
		t.Errorf("token = %q, want refreshed %q", sess.Token, newToken)
	}
	if !refreshCalled {
		t.Error("refresh endpoint was not called")
	}
}

func TestResolveSession_RefreshExpiredFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid or expired session", http.StatusUnauthorized)
	}))
	defer srv.Close()

	// Save session that is fully expired (1 hour ago).
	expired := time.Now().Add(-1 * time.Hour)
	if err := auth.SaveSession("http://tunnel:8080", "expired-token", "user1", "admin", srv.URL, expired); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	_, err := ResolveSession("", "test")
	if err == nil {
		t.Fatal("expected error for expired session with failed refresh, got nil")
	}
	if !strings.Contains(err.Error(), "session expired and refresh failed") {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "run login") {
		t.Errorf("error should mention re-login: %v", err)
	}
}

func TestResolveSession_Flag_WithSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := auth.SaveSession("http://flag:8080", "tok2", "user2", "admin", "", time.Time{}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	sess, err := ResolveSession("http://flag:8080", "test")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if sess.URL != "http://flag:8080" {
		t.Errorf("url = %q, want %q", sess.URL, "http://flag:8080")
	}
	if sess.Token != "tok2" {
		t.Errorf("token = %q, want %q", sess.Token, "tok2")
	}
}

func TestResolveSession_Flag_NoSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	sess, err := ResolveSession("http://explicit:8080", "test")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if sess.URL != "http://explicit:8080" {
		t.Errorf("url = %q, want %q", sess.URL, "http://explicit:8080")
	}
	if sess.Token != "" {
		t.Errorf("token = %q, want empty", sess.Token)
	}
}

func TestResolveSession_TrailingSlash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := auth.SaveSession("http://host:8080", "tok", "user", "admin", "", time.Time{}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	sess, err := ResolveSession("http://host:8080/", "test")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if sess.URL != "http://host:8080" {
		t.Errorf("url = %q, want %q (trailing slash not trimmed)", sess.URL, "http://host:8080")
	}
	if sess.Token != "tok" {
		t.Errorf("token = %q, want %q", sess.Token, "tok")
	}
}