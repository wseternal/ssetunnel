package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNeedsRefresh(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{"zero time (old session)", time.Time{}, false},
		{"expires in 30 days", time.Now().Add(30 * 24 * time.Hour), false},
		{"expires in 8 days", time.Now().Add(8 * 24 * time.Hour), false},
		{"expires in 6 days", time.Now().Add(6 * 24 * time.Hour), true},
		{"expires in 1 day", time.Now().Add(24 * time.Hour), true},
		{"already expired", time.Now().Add(-24 * time.Hour), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NeedsRefresh(tt.expiresAt)
			if got != tt.want {
				t.Errorf("NeedsRefresh(%v) = %v, want %v", tt.expiresAt, got, tt.want)
			}
		})
	}
}

func TestRefreshSession_Success(t *testing.T) {
	newToken := "new-refreshed-token-123"
	newExpiresAt := time.Now().UTC().Add(30 * 24 * time.Hour)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/console/api/v1/refresh-session" {
			t.Errorf("path = %s", r.URL.Path)
		}
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer old-token" {
			t.Errorf("auth = %q", authHeader)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"token":      newToken,
			"expires_at": newExpiresAt.Format(time.RFC3339),
		})
	}))
	defer srv.Close()

	token, expiresAt, err := RefreshSession(srv.URL, "old-token")
	if err != nil {
		t.Fatalf("RefreshSession: %v", err)
	}
	if token != newToken {
		t.Errorf("token = %q, want %q", token, newToken)
	}
	// Compare within 1 second (JSON marshaling may lose sub-second precision)
	if diff := expiresAt.Sub(newExpiresAt); diff > time.Second || diff < -time.Second {
		t.Errorf("expiresAt = %v, want ~%v (diff=%v)", expiresAt, newExpiresAt, diff)
	}
}

func TestRefreshSession_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid or expired session", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, _, err := RefreshSession(srv.URL, "expired-token")
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestValidateConsoleURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"http localhost", "http://localhost:8081", false},
		{"http 127.0.0.1", "http://127.0.0.1:8081", false},
		{"http [::1]", "http://[::1]:8081", false},
		{"https remote host", "https://tunnel.example.com:8081", false},
		{"https no port", "https://tunnel.example.com", false},
		{"http remote host rejected", "http://tunnel.example.com:8081", true},
		{"http internal IP rejected", "http://10.0.0.1:8081", true},
		{"ftp scheme rejected", "ftp://server:21", true},
		{"file scheme rejected", "file:///etc/passwd", true},
		{"empty string", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConsoleURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateConsoleURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestRefreshSession_InvalidConsoleURL(t *testing.T) {
	_, _, err := RefreshSession("http://internal.corp:8081", "some-token")
	if err == nil {
		t.Fatal("expected error for non-localhost http URL")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error should mention https requirement: %v", err)
	}
}
