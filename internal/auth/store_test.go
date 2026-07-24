package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/migrations"
	orcapostgres "github.com/visdomtech/orcacommon/postgres"
)

func TestAuthStore_TokenAndPINAndSession(t *testing.T) {
	ctx := context.Background()

	// Spin up testcontainer postgres pool with automatic migrations
	dbcfg := orcapostgres.DBConfig{
		DatabaseURLTemplate: "postgres:tc:",
	}
	pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
	if err != nil {
		t.Fatalf("failed to open pool with testcontainer: %v", err)
	}

	store := auth.NewStore(pool)

	// 1. PIN Test
	pinStr, err := auth.GeneratePIN()
	if err != nil {
		t.Fatalf("GeneratePIN failed: %v", err)
	}
	if len(pinStr) < 8 {
		t.Errorf("expected PIN length >= 8, got %d", len(pinStr))
	}

	err = store.CreatePIN(ctx, pinStr, "agent", 15*time.Minute)
	if err != nil {
		t.Fatalf("CreatePIN failed: %v", err)
	}

	// Verify and use PIN (first time -> success)
	role, err := store.VerifyAndUsePIN(ctx, pinStr)
	if err != nil {
		t.Fatalf("VerifyAndUsePIN first call failed: %v", err)
	}
	if role != "agent" {
		t.Errorf("expected role 'agent', got %q", role)
	}

	// Single-use check (second time -> fails)
	_, err = store.VerifyAndUsePIN(ctx, pinStr)
	if err == nil {
		t.Errorf("expected second VerifyAndUsePIN call to fail, but succeeded")
	}

	// 2. Bearer Token Test
	rawToken, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	err = store.CreateToken(ctx, rawToken, "user", "test user token", nil)
	if err != nil {
		t.Fatalf("CreateToken failed: %v", err)
	}

	// Validate token (cache miss -> DB lookup -> cache fill)
	tokInfo, err := store.ValidateToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("ValidateToken first call failed: %v", err)
	}
	if tokInfo.Role != "user" {
		t.Errorf("expected role 'user', got %q", tokInfo.Role)
	}

	// Validate token again (cache hit)
	tokInfo, err = store.ValidateToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("ValidateToken second call failed: %v", err)
	}
	if tokInfo.Role != "user" {
		t.Errorf("expected role 'user', got %q", tokInfo.Role)
	}

	// Revoke token
	err = store.RevokeToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("RevokeToken failed: %v", err)
	}

	// Validate revoked token -> should fail
	_, err = store.ValidateToken(ctx, rawToken)
	if err == nil {
		t.Errorf("expected ValidateToken on revoked token to fail, but succeeded")
	}

	// 3. Admin Session Test
	sessToken, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken for session failed: %v", err)
	}

	err = store.CreateAdminSession(ctx, sessToken, 12*time.Hour)
	if err != nil {
		t.Fatalf("CreateAdminSession failed: %v", err)
	}

	err = store.ValidateAdminSession(ctx, sessToken)
	if err != nil {
		t.Fatalf("ValidateAdminSession failed: %v", err)
	}

	// Invalid session token check
	err = store.ValidateAdminSession(ctx, "invalid-session-token")
	if err == nil {
		t.Errorf("expected ValidateAdminSession with invalid token to fail, but succeeded")
	}
}

func TestTOTPVerification(t *testing.T) {
	secret := "JBSWY3DPEHPK3PXP" // standard base32 test secret
	code, err := auth.GenerateTOTPCode(secret)
	if err != nil {
		t.Fatalf("GenerateTOTPCode failed: %v", err)
	}

	valid := auth.VerifyTOTP(secret, code)
	if !valid {
		t.Errorf("expected TOTP code %q to be valid for secret %q", code, secret)
	}

	invalid := auth.VerifyTOTP(secret, "000000")
	if invalid {
		t.Errorf("expected invalid TOTP code to fail")
	}
}
