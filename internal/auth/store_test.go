package auth_test

import (
	"context"
	"testing"

	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/migrations"
	orcapostgres "github.com/visdomtech/orcacommon/postgres"
)

func TestAuthStore_TokenLifecycle(t *testing.T) {
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

	// Create token
	rawToken, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	err = store.CreateToken(ctx, rawToken, "user", "test user token", nil)
	if err != nil {
		t.Fatalf("CreateToken failed: %v", err)
	}

	// Validate token (DB lookup)
	tokInfo, err := store.ValidateToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("ValidateToken first call failed: %v", err)
	}
	if tokInfo.Role != "user" {
		t.Errorf("expected role 'user', got %q", tokInfo.Role)
	}

	// Validate token again (DB lookup, no cache)
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
