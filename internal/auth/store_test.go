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

func TestEnsureAdminUser_SeedsOnEmptyDB(t *testing.T) {
	ctx := context.Background()

	dbcfg := orcapostgres.DBConfig{
		DatabaseURLTemplate: "postgres:tc:",
	}
	pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
	if err != nil {
		t.Fatalf("failed to open pool: %v", err)
	}

	store := auth.NewStore(pool)

	// First call: no admin exists — should seed one.
	pw, err := store.EnsureAdminUser(ctx)
	if err != nil {
		t.Fatalf("EnsureAdminUser first call: %v", err)
	}
	if pw == "" {
		t.Fatal("expected non-empty password on first call, got empty string")
	}

	// Verify the seeded admin can authenticate.
	user, err := store.ValidatePassword(ctx, "admin", pw)
	if err != nil {
		t.Fatalf("ValidatePassword for seeded admin: %v", err)
	}
	if user.Role != "admin" {
		t.Errorf("expected role 'admin', got %q", user.Role)
	}

	// Second call: admin already exists — should return empty password.
	pw2, err := store.EnsureAdminUser(ctx)
	if err != nil {
		t.Fatalf("EnsureAdminUser second call: %v", err)
	}
	if pw2 != "" {
		t.Errorf("expected empty password on second call, got %q", pw2)
	}
}

func TestAgentConfig_ArrayScanRoundTrip(t *testing.T) {
	ctx := context.Background()

	dbcfg := orcapostgres.DBConfig{
		DatabaseURLTemplate: "postgres:tc:",
	}
	pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
	if err != nil {
		t.Fatalf("failed to open pool: %v", err)
	}

	store := auth.NewStore(pool)

	// Create a config with allowed_targets array
	targets := []string{"127.0.0.1:*", "10.0.0.1:22"}
	cfg, err := store.CreateAgentConfig(ctx, "testbox", "test agent", targets)
	if err != nil {
		t.Fatalf("CreateAgentConfig failed: %v", err)
	}
	if len(cfg.AllowedTargets) != 2 {
		t.Fatalf("expected 2 allowed targets, got %d", len(cfg.AllowedTargets))
	}

	// Read back by agent_id - this is the path that fails in production
	got, err := store.GetAgentConfig(ctx, "testbox")
	if err != nil {
		t.Fatalf("GetAgentConfig by agent_id failed: %v", err)
	}
	if len(got.AllowedTargets) != 2 || got.AllowedTargets[0] != "127.0.0.1:*" || got.AllowedTargets[1] != "10.0.0.1:22" {
		t.Errorf("AllowedTargets round-trip mismatch: got %v, want %v", got.AllowedTargets, targets)
	}

	// Read back via ListAgentConfigs (includes seeded default row)
	all, err := store.ListAgentConfigs(ctx)
	if err != nil {
		t.Fatalf("ListAgentConfigs failed: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("expected at least 2 configs (testbox + default), got %d", len(all))
	}
	// Find our testbox row and verify its targets
	var found bool
	for _, c := range all {
		if c.AgentID != nil && *c.AgentID == "testbox" {
			found = true
			if len(c.AllowedTargets) != 2 {
				t.Errorf("ListAgentConfigs testbox AllowedTargets length: got %d, want 2", len(c.AllowedTargets))
			}
		}
	}
	if !found {
		t.Error("testbox config not found in ListAgentConfigs results")
	}

	// Read default (NULL) row - the exact scenario from the bug report
	_, err = store.GetDefaultAgentConfig(ctx)
	if err != nil {
		t.Fatalf("GetDefaultAgentConfig failed: %v", err)
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
