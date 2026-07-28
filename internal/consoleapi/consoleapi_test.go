package consoleapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	totpSecret := "JBSWY3DPEHPK3PXP" // test secret

	router := consoleapi.NewRouter(store, reg, totpSecret)

	// 1. POST /api/v1/login with invalid code -> 401
	loginBody, _ := json.Marshal(map[string]string{"totp_code": "000000"})
	req := httptest.NewRequest("POST", "/api/v1/login", bytes.NewReader(loginBody))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected login 401 for bad TOTP, got %d", rec.Code)
	}

	// 2. POST /api/v1/login with valid code -> 200 + ssetunnel_session cookie
	code, err := auth.GenerateTOTPCode(totpSecret)
	if err != nil {
		t.Fatalf("failed to generate TOTP code: %v", err)
	}
	loginBody, _ = json.Marshal(map[string]string{"totp_code": code})
	req = httptest.NewRequest("POST", "/api/v1/login", bytes.NewReader(loginBody))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected login 200 for valid TOTP, got %d", rec.Code)
	}

	cookies := rec.Result().Cookies()
	var sessCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == server.SessionCookieName {
			sessCookie = c
			break
		}
	}
	if sessCookie == nil {
		t.Fatalf("expected ssetunnel_session cookie set on successful login")
	}

	// 3. POST /api/v1/tokens (create token)
	createBody, _ := json.Marshal(map[string]string{"role": "agent", "description": "test agent"})
	req = httptest.NewRequest("POST", "/api/v1/tokens", bytes.NewReader(createBody))
	req.AddCookie(sessCookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 creating token, got %d: %s", rec.Code, rec.Body.String())
	}

	var createResp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &createResp)
	rawTok, ok := createResp["token"].(string)
	if !ok || len(rawTok) == 0 {
		t.Errorf("expected raw token in create response")
	}

	// 4. GET /api/v1/tokens (list tokens)
	req = httptest.NewRequest("GET", "/api/v1/tokens", nil)
	req.AddCookie(sessCookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 listing tokens, got %d", rec.Code)
	}

	var tokensList []map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &tokensList)
	if len(tokensList) == 0 {
		t.Errorf("expected non-empty tokens list")
	}

	// 5. POST /api/v1/enroll (generate PIN)
	req = httptest.NewRequest("POST", "/api/v1/enroll", bytes.NewReader([]byte(`{"role":"agent"}`)))
	req.AddCookie(sessCookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 enrolling PIN, got %d", rec.Code)
	}

	var enrollResp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &enrollResp)
	if len(enrollResp["pin"]) < 8 {
		t.Errorf("expected valid PIN in enroll response, got %q", enrollResp["pin"])
	}

	// 6. GET /api/v1/sessions
	req = httptest.NewRequest("GET", "/api/v1/sessions", nil)
	req.AddCookie(sessCookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 getting sessions, got %d", rec.Code)
	}
}

func TestLoginTOTPNotConfigured(t *testing.T) {
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

	// Router with empty totpSecret → TOTP not configured
	router := consoleapi.NewRouter(store, reg, "")

	loginBody, _ := json.Marshal(map[string]string{"totp_code": "123456"})
	req := httptest.NewRequest("POST", "/api/v1/login", bytes.NewReader(loginBody))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when TOTP not configured, got %d", rec.Code)
	}
}
