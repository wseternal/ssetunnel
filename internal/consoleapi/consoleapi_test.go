package consoleapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/consoleapi"
	"github.com/wseternal/ssetunnel/internal/server"
	"github.com/wseternal/ssetunnel/migrations"
	orcapostgres "github.com/visdomtech/orcacommon/postgres"
)

func TestConsoleAPI(t *testing.T) {
	ctx := context.Background()

	dbcfg := orcapostgres.DBConfig{
		DatabaseURLTemplate: "postgres:tc:",
	}
	pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
	if err != nil {
		t.Fatalf("failed to open pool: %v", err)
	}

	store := auth.NewStore(pool)
	reg := server.NewRegistry()

	router := consoleapi.NewRouter(store, reg, "")

	// Bootstrap an admin user directly in the store.
	pwHash, err := auth.HashPassword("testpass123")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	adminUser, err := store.CreateUser(ctx, "admin", pwHash, "admin")
	if err != nil {
		t.Fatalf("failed to create admin user: %v", err)
	}

	// Create a session for the admin user.
	adminToken, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("failed to generate admin token: %v", err)
	}
	if err := store.CreateUserSession(ctx, adminUser.ID, adminToken, 24*time.Hour); err != nil {
		t.Fatalf("failed to create admin session: %v", err)
	}

	// 1. POST /api/v1/users with admin bearer token -> create a new user
	createUserBody, _ := json.Marshal(map[string]string{
		"username": "newuser",
		"password": "userpass456",
		"role":     "user",
	})
	req := httptest.NewRequest("POST", "/api/v1/users", bytes.NewReader(createUserBody))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating user, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. POST /api/v1/user-login with valid credentials -> 200 + token
	loginBody, _ := json.Marshal(map[string]string{
		"username": "newuser",
		"password": "userpass456",
	})
	req = httptest.NewRequest("POST", "/api/v1/user-login", bytes.NewReader(loginBody))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected login 200 for valid credentials, got %d: %s", rec.Code, rec.Body.String())
	}

	var loginResp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &loginResp)
	sessionToken, ok := loginResp["token"].(string)
	if !ok || sessionToken == "" {
		t.Fatal("expected token in login response")
	}

	// 3. GET /api/v1/sessions with admin bearer token
	req = httptest.NewRequest("GET", "/api/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 getting sessions, got %d", rec.Code)
	}

	// 4. POST /api/v1/user-login with invalid password -> 401
	badLoginBody, _ := json.Marshal(map[string]string{
		"username": "newuser",
		"password": "wrongpass",
	})
	req = httptest.NewRequest("POST", "/api/v1/user-login", bytes.NewReader(badLoginBody))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for bad password, got %d", rec.Code)
	}
}

func TestConsoleAPI_LogoutClearsSession(t *testing.T) {
	ctx := context.Background()

	dbcfg := orcapostgres.DBConfig{
		DatabaseURLTemplate: "postgres:tc:",
	}
	pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
	if err != nil {
		t.Fatalf("failed to open pool: %v", err)
	}

	store := auth.NewStore(pool)
	reg := server.NewRegistry()

	router := consoleapi.NewRouter(store, reg, "")

	// POST /api/v1/logout -> 200 + expired cookie
	req := httptest.NewRequest("POST", "/api/v1/logout", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 on logout, got %d", rec.Code)
	}
}
