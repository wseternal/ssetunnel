package consoleapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

	router := consoleapi.NewRouter(store, reg)

	// Bootstrap an admin user directly in the store.
	pwHash, err := auth.HashPassword("testpass123")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	adminUser, err := store.CreateUser(ctx, "admin", pwHash, "admin", true, true)
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

	router := consoleapi.NewRouter(store, reg)

	// POST /api/v1/logout -> 200 + expired cookie
	req := httptest.NewRequest("POST", "/api/v1/logout", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 on logout, got %d", rec.Code)
	}
}

// helper: set up pool, store, router, and a test user with a session.
// Uses t.Name() to create a unique username per test to avoid collisions
// when tests share a testcontainer database.
func setupTestEnv(t *testing.T) (http.Handler, *auth.Store, *auth.UserInfo, string) {
	t.Helper()
	ctx := context.Background()
	dbcfg := orcapostgres.DBConfig{DatabaseURLTemplate: "postgres:tc:"}
	pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
	if err != nil {
		t.Fatalf("failed to open pool: %v", err)
	}
	store := auth.NewStore(pool)
	reg := server.NewRegistry()
	router := consoleapi.NewRouter(store, reg)

	// Derive a unique username from the test name (sanitized for DB).
	uname := sanitizeUsername(t.Name())
	pwHash, _ := auth.HashPassword("testpass123")
	user, err := store.CreateUser(ctx, uname, pwHash, "admin", true, true)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	token, _ := auth.GenerateToken()
	_ = store.CreateUserSession(ctx, user.ID, token, 24*time.Hour)

	return router, store, user, token
}

// sanitizeUsername converts a test name into a valid PostgreSQL username (<=63 chars, lowercase alnum + underscore).
func sanitizeUsername(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	s := b.String()
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

func TestLoginCheck(t *testing.T) {
	router, store, user, _ := setupTestEnv(t)
	ctx := context.Background()
	uname := sanitizeUsername(t.Name())

	// User without TOTP enrolled.
	body, _ := json.Marshal(map[string]string{"username": uname})
	req := httptest.NewRequest("POST", "/api/v1/user-login-check", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp map[string]bool
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["totp_required"] {
		t.Error("expected totp_required=false for user without TOTP")
	}

	// Set TOTP secret and check again.
	_ = store.SetTOTPSecret(ctx, user.ID, "JBSWY3DPEHPK3PXP")
	req = httptest.NewRequest("POST", "/api/v1/user-login-check", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp["totp_required"] {
		t.Error("expected totp_required=true for user with TOTP")
	}

	// Non-existent user should return true (anti-enumeration).
	body2, _ := json.Marshal(map[string]string{"username": "nonexistent"})
	req = httptest.NewRequest("POST", "/api/v1/user-login-check", bytes.NewReader(body2))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp["totp_required"] {
		t.Error("expected totp_required=true for non-existent user (anti-enumeration)")
	}
}

func TestLogin_NoTOTP(t *testing.T) {
	router, _, _, _ := setupTestEnv(t)
	uname := sanitizeUsername(t.Name())

	// User without TOTP should login without TOTP code.
	body, _ := json.Marshal(map[string]string{"username": uname, "password": "testpass123"})
	req := httptest.NewRequest("POST", "/api/v1/user-login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["totp_enrolled"] != false {
		t.Error("expected totp_enrolled=false")
	}
}

func TestLogin_PerUserTOTP(t *testing.T) {
	router, store, user, _ := setupTestEnv(t)
	ctx := context.Background()
	uname := sanitizeUsername(t.Name())

	secret := "JBSWY3DPEHPK3PXP"
	_ = store.SetTOTPSecret(ctx, user.ID, secret)

	// Login without TOTP code should fail.
	body, _ := json.Marshal(map[string]string{"username": uname, "password": "testpass123"})
	req := httptest.NewRequest("POST", "/api/v1/user-login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without TOTP code, got %d", rec.Code)
	}

	// Login with valid TOTP code should succeed.
	code, _ := auth.GenerateTOTPCode(secret)
	body, _ = json.Marshal(map[string]string{"username": uname, "password": "testpass123", "totp_code": code})
	req = httptest.NewRequest("POST", "/api/v1/user-login", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid TOTP, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["totp_enrolled"] != true {
		t.Error("expected totp_enrolled=true")
	}

	// Login with invalid TOTP code should fail.
	body, _ = json.Marshal(map[string]string{"username": uname, "password": "testpass123", "totp_code": "000000"})
	req = httptest.NewRequest("POST", "/api/v1/user-login", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with bad TOTP, got %d", rec.Code)
	}
}

func TestLogin_RecoveryCode(t *testing.T) {
	router, store, user, _ := setupTestEnv(t)
	ctx := context.Background()
	uname := sanitizeUsername(t.Name())

	// Set up TOTP + recovery codes.
	secret := "JBSWY3DPEHPK3PXP"
	_ = store.SetTOTPSecret(ctx, user.ID, secret)

	codes, _ := auth.GenerateRecoveryCodes(3)
	digests := make([]string, len(codes))
	for i, c := range codes {
		digests[i] = store.RecoveryCodeDigest(c)
	}
	_ = store.SaveRecoveryCodes(ctx, user.ID, digests)

	// Login with recovery code (wrong TOTP but valid recovery).
	body, _ := json.Marshal(map[string]string{
		"username": uname, "password": "testpass123", "totp_code": codes[0],
	})
	req := httptest.NewRequest("POST", "/api/v1/user-login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with recovery code, got %d: %s", rec.Code, rec.Body.String())
	}

	// Same recovery code should not work again (single-use).
	body, _ = json.Marshal(map[string]string{
		"username": uname, "password": "testpass123", "totp_code": codes[0],
	})
	req = httptest.NewRequest("POST", "/api/v1/user-login", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for reused recovery code, got %d", rec.Code)
	}

	// Second recovery code should still work.
	body, _ = json.Marshal(map[string]string{
		"username": uname, "password": "testpass123", "totp_code": codes[1],
	})
	req = httptest.NewRequest("POST", "/api/v1/user-login", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with second recovery code, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNonAdminSessionFiltering(t *testing.T) {
	ctx := context.Background()

	dbcfg := orcapostgres.DBConfig{DatabaseURLTemplate: "postgres:tc:"}
	pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
	if err != nil {
		t.Fatalf("failed to open pool: %v", err)
	}
	store := auth.NewStore(pool)
	reg := server.NewRegistry()
	router := consoleapi.NewRouter(store, reg)

	// Create admin user + session.
	adminHash, _ := auth.HashPassword("adminpass123")
	adminUser, _ := store.CreateUser(ctx, "nonadmin_test_admin", adminHash, "admin", true, true)
	adminToken, _ := auth.GenerateToken()
	_ = store.CreateUserSession(ctx, adminUser.ID, adminToken, 24*time.Hour)

	// Create regular user + session.
	userHash, _ := auth.HashPassword("userpass12345")
	regUser, _ := store.CreateUser(ctx, "nonadmin_test_user", userHash, "user", true, true)
	userToken, _ := auth.GenerateToken()
	_ = store.CreateUserSession(ctx, regUser.ID, userToken, 24*time.Hour)

	// Create sessions in the registry with different user attributions.
	adminSess := server.NewSession("sess-admin-1")
	adminSess.SetUserID(adminUser.ID)
	reg.Replace(adminSess)

	userSess := server.NewSession("sess-user-1")
	userSess.SetUserID(regUser.ID)
	reg.Replace(userSess)

	unattrSess := server.NewSession("sess-unattr")
	// userID remains 0 (unattributed)
	reg.Replace(unattrSess)

	// Admin sees ALL sessions.
	req := httptest.NewRequest("GET", "/api/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var adminSessions []map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &adminSessions)
	if len(adminSessions) != 3 {
		t.Errorf("admin: expected 3 sessions, got %d", len(adminSessions))
	}

	// Regular user sees ONLY their own sessions.
	req = httptest.NewRequest("GET", "/api/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("user: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var userSessions []map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &userSessions)
	if len(userSessions) != 1 {
		t.Errorf("user: expected 1 session, got %d", len(userSessions))
	}
	if len(userSessions) > 0 && userSessions[0]["id"] != "sess-user-1" {
		t.Errorf("user: expected sess-user-1, got %v", userSessions[0]["id"])
	}

	// Regular user can also list agents (read-only, all configs).
	_, _ = store.CreateAgentConfig(ctx, "test-agent", "test", []string{"*"})

	req = httptest.NewRequest("GET", "/api/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("user agents: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var userAgents []map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &userAgents)
	// Should see at least the default config + the one we created.
	if len(userAgents) < 2 {
		t.Errorf("user agents: expected >=2 configs, got %d", len(userAgents))
	}
	// Non-admin users should have allowed_targets redacted (nil/null).
	for _, agent := range userAgents {
		if targets, ok := agent["allowed_targets"]; ok && targets != nil {
			t.Errorf("user agents: expected allowed_targets to be redacted, got %v", targets)
		}
	}

	// Admin sees allowed_targets populated.
	req = httptest.NewRequest("GET", "/api/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin agents: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var adminAgents []map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &adminAgents)
	foundTargets := false
	for _, agent := range adminAgents {
		if targets, ok := agent["allowed_targets"]; ok && targets != nil {
			foundTargets = true
			break
		}
	}
	if !foundTargets {
		t.Errorf("admin agents: expected at least one config with allowed_targets populated")
	}

	// Regular user CANNOT create agents (admin-only).
	body, _ := json.Marshal(map[string]interface{}{"agent_id": "rogue", "allowed_targets": []string{"*"}})
	req = httptest.NewRequest("POST", "/api/v1/agents", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+userToken)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("user POST agents: expected 401, got %d", rec.Code)
	}

	// Regular user CANNOT access user management.
	req = httptest.NewRequest("GET", "/api/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("user GET users: expected 401, got %d", rec.Code)
	}
}

func TestTOTPSetupFlow(t *testing.T) {
	router, _, _, token := setupTestEnv(t)

	// 1. Check status — not enrolled.
	req := httptest.NewRequest("GET", "/api/v1/totp/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on status, got %d", rec.Code)
	}
	var statusResp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &statusResp)
	if statusResp["enrolled"] != false {
		t.Error("expected enrolled=false initially")
	}

	// 2. Begin setup — get secret + key_url.
	req = httptest.NewRequest("POST", "/api/v1/totp/begin-setup", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on begin-setup, got %d", rec.Code)
	}
	var setupResp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &setupResp)
	secret := setupResp["secret"]
	if secret == "" {
		t.Fatal("expected non-empty secret")
	}

	// 3. Verify setup with valid code.
	code, _ := auth.GenerateTOTPCode(secret)
	body, _ := json.Marshal(map[string]string{"secret": secret, "code": code})
	req = httptest.NewRequest("POST", "/api/v1/totp/verify-setup", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on verify-setup, got %d: %s", rec.Code, rec.Body.String())
	}
	var verifyResp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &verifyResp)
	rc, ok := verifyResp["recovery_codes"].([]interface{})
	if !ok || len(rc) == 0 {
		t.Fatal("expected recovery_codes in response")
	}

	// 4. Check status — now enrolled.
	req = httptest.NewRequest("GET", "/api/v1/totp/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &statusResp)
	if statusResp["enrolled"] != true {
		t.Error("expected enrolled=true after setup")
	}

	// 5. Verify setup after enrollment should fail (409 Conflict — must DELETE first).
	body, _ = json.Marshal(map[string]string{"secret": secret, "code": "000000"})
	req = httptest.NewRequest("POST", "/api/v1/totp/verify-setup", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 on verify-setup while enrolled, got %d", rec.Code)
	}

	// 6. Begin setup again while enrolled should fail (409 Conflict).
	req = httptest.NewRequest("POST", "/api/v1/totp/begin-setup", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 on begin-setup while enrolled, got %d", rec.Code)
	}
}
