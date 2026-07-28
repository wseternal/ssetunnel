package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/server"
	"github.com/wseternal/ssetunnel/migrations"
	orcapostgres "github.com/visdomtech/orcacommon/postgres"
)

func TestAuthMiddlewares(t *testing.T) {
	ctx := context.Background()

	dbcfg := orcapostgres.DBConfig{
		DatabaseURLTemplate: "postgres:tc:",
	}
	pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
	if err != nil {
		t.Fatalf("failed to open pool: %v", err)
	}

	store := auth.NewStore(pool)

	// Create valid agent token
	agentToken, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("failed to generate agent token: %v", err)
	}
	err = store.CreateToken(ctx, agentToken, "agent", "test agent", nil)
	if err != nil {
		t.Fatalf("failed to create agent token: %v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// 1. AgentAuthMiddleware without token -> 401
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/events?id=test", nil)
	server.AgentAuthMiddleware(store)(handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401 without token, got %d", rec.Code)
	}

	// 2. AgentAuthMiddleware with valid token in header -> 200
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/events?id=test", nil)
	req.Header.Set("Authorization", "Bearer "+agentToken)
	server.AgentAuthMiddleware(store)(handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 with valid token header, got %d", rec.Code)
	}

	// 3. AgentAuthMiddleware with valid token in query param -> 200
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/events?id=test&token="+agentToken, nil)
	server.AgentAuthMiddleware(store)(handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 with valid token query param, got %d", rec.Code)
	}

	// 4. AgentAuthMiddleware with nil store (auth disabled) -> 200
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/events?id=test", nil)
	server.AgentAuthMiddleware(nil)(handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 with auth disabled (nil store), got %d", rec.Code)
	}

	// 5. AgentAuthMiddleware with valid PIN -> 200 + X-SSET-Token header
	pinStr, err := auth.GeneratePIN()
	if err != nil {
		t.Fatalf("failed to generate PIN: %v", err)
	}
	if err := store.CreatePIN(ctx, pinStr, "agent", 15*time.Minute); err != nil {
		t.Fatalf("failed to create PIN: %v", err)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/events?id=test", nil)
	req.Header.Set("Authorization", "Bearer "+pinStr)
	server.AgentAuthMiddleware(store)(handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 with valid PIN, got %d", rec.Code)
	}
	upgradedToken := rec.Header().Get("X-SSET-Token")
	if upgradedToken == "" {
		t.Error("expected X-SSET-Token header on PIN redemption, got empty")
	}

	// 6. The upgraded token should work for subsequent requests
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/events?id=test", nil)
	req.Header.Set("Authorization", "Bearer "+upgradedToken)
	server.AgentAuthMiddleware(store)(handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 with upgraded token, got %d", rec.Code)
	}

	// 7. Same PIN again -> 401 (single-use, already consumed)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/events?id=test", nil)
	req.Header.Set("Authorization", "Bearer "+pinStr)
	server.AgentAuthMiddleware(store)(handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401 with already-used PIN, got %d", rec.Code)
	}
}
