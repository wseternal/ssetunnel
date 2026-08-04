package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wseternal/ssetunnel/internal/auth"
)

func TestUserSessionMiddleware_NilStore_InjectsSyntheticAdmin(t *testing.T) {
	t.Parallel()

	var capturedSession *auth.UserSessionInfo
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSession = UserSessionFromContext(r)
		w.WriteHeader(http.StatusOK)
	})

	// When store is nil (auth disabled), middleware should inject a synthetic admin session
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/console/api/v1/me", nil)
	UserSessionMiddleware(nil)(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 with auth disabled, got %d", rec.Code)
	}
	if capturedSession == nil {
		t.Fatal("expected synthetic admin session in context, got nil")
	}
	if capturedSession.Role != "admin" {
		t.Errorf("expected synthetic session role 'admin', got %q", capturedSession.Role)
	}
	// Synthetic admin should have full permissions
	if !capturedSession.PermConnect {
		t.Error("expected synthetic session to have PermConnect")
	}
	if !capturedSession.PermAgent {
		t.Error("expected synthetic session to have PermAgent")
	}
}
