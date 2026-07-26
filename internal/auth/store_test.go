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

func TestSetTOTPSecret(t *testing.T) {
	ctx := context.Background()
	dbcfg := orcapostgres.DBConfig{DatabaseURLTemplate: "postgres:tc:"}
	pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
	if err != nil {
		t.Fatalf("failed to open pool: %v", err)
	}
	store := auth.NewStore(pool)

	// Create a test user.
	hash, _ := auth.HashPassword("testpass")
	user, err := store.CreateUser(ctx, "totpuser", hash, "user", true, false)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// Set TOTP secret.
	secret := "JBSWY3DPEHPK3PXP"
	if err := store.SetTOTPSecret(ctx, user.ID, secret); err != nil {
		t.Fatalf("SetTOTPSecret failed: %v", err)
	}

	// Read back and verify.
	u, err := store.GetUserByUsername(ctx, "totpuser")
	if err != nil {
		t.Fatalf("GetUserByUsername failed: %v", err)
	}
	if u.TOTPSecret != secret {
		t.Errorf("expected totp_secret=%q, got %q", secret, u.TOTPSecret)
	}

	// UserTOTPEnrolled should return true.
	enrolled, found, err := store.UserTOTPEnrolled(ctx, "totpuser")
	if err != nil {
		t.Fatalf("UserTOTPEnrolled failed: %v", err)
	}
	if !found {
		t.Error("expected found=true for existing user")
	}
	if !enrolled {
		t.Error("expected UserTOTPEnrolled to return true")
	}

	// Clear TOTP secret.
	if err := store.SetTOTPSecret(ctx, user.ID, ""); err != nil {
		t.Fatalf("SetTOTPSecret clear failed: %v", err)
	}
	u, _ = store.GetUserByUsername(ctx, "totpuser")
	if u.TOTPSecret != "" {
		t.Errorf("expected empty totp_secret after clear, got %q", u.TOTPSecret)
	}

	// Non-existent user.
	if err := store.SetTOTPSecret(ctx, 999999, "x"); err == nil {
		t.Error("expected error for non-existent user")
	}
}

func TestRecoveryCodesRoundTrip(t *testing.T) {
	ctx := context.Background()
	dbcfg := orcapostgres.DBConfig{DatabaseURLTemplate: "postgres:tc:"}
	pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
	if err != nil {
		t.Fatalf("failed to open pool: %v", err)
	}
	store := auth.NewStore(pool)

	// Create a test user.
	hash, _ := auth.HashPassword("testpass")
	user, err := store.CreateUser(ctx, "recoveryuser", hash, "user", true, false)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// Generate recovery codes.
	codes, err := auth.GenerateRecoveryCodes(8)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes failed: %v", err)
	}
	if len(codes) != 8 {
		t.Fatalf("expected 8 codes, got %d", len(codes))
	}

	// Compute digests and store.
	digests := make([]string, len(codes))
	for i, c := range codes {
		digests[i] = store.RecoveryCodeDigest(c)
	}
	if err := store.SaveRecoveryCodes(ctx, user.ID, digests); err != nil {
		t.Fatalf("SaveRecoveryCodes failed: %v", err)
	}

	// Count should be 8.
	count, err := store.CountUnusedRecoveryCodes(ctx, user.ID)
	if err != nil {
		t.Fatalf("CountUnusedRecoveryCodes failed: %v", err)
	}
	if count != 8 {
		t.Errorf("expected 8 unused codes, got %d", count)
	}

	// Consume first code — should succeed.
	ok, err := store.ConsumeRecoveryCode(ctx, user.ID, codes[0])
	if err != nil {
		t.Fatalf("ConsumeRecoveryCode failed: %v", err)
	}
	if !ok {
		t.Error("expected first consume to succeed")
	}

	// Consume same code again — should fail.
	ok, err = store.ConsumeRecoveryCode(ctx, user.ID, codes[0])
	if err != nil {
		t.Fatalf("ConsumeRecoveryCode second call error: %v", err)
	}
	if ok {
		t.Error("expected second consume of same code to fail")
	}

	// Count should be 7.
	count, _ = store.CountUnusedRecoveryCodes(ctx, user.ID)
	if count != 7 {
		t.Errorf("expected 7 unused codes after consume, got %d", count)
	}

	// Consume wrong code — should fail.
	ok, err = store.ConsumeRecoveryCode(ctx, user.ID, "wrongcode")
	if err != nil {
		t.Fatalf("ConsumeRecoveryCode wrong code error: %v", err)
	}
	if ok {
		t.Error("expected wrong code to fail")
	}

	// Delete all codes.
	if err := store.DeleteRecoveryCodes(ctx, user.ID); err != nil {
		t.Fatalf("DeleteRecoveryCodes failed: %v", err)
	}
	count, _ = store.CountUnusedRecoveryCodes(ctx, user.ID)
	if count != 0 {
		t.Errorf("expected 0 unused codes after delete, got %d", count)
	}
}

func TestGenerateRecoveryCodes(t *testing.T) {
	codes, err := auth.GenerateRecoveryCodes(5)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes failed: %v", err)
	}
	if len(codes) != 5 {
		t.Fatalf("expected 5 codes, got %d", len(codes))
	}
	for i, c := range codes {
		if len(c) != 10 {
			t.Errorf("code[%d] length = %d, want 10", i, len(c))
		}
	}
	// All codes should be unique.
	seen := make(map[string]bool)
	for _, c := range codes {
		if seen[c] {
			t.Errorf("duplicate recovery code: %s", c)
		}
		seen[c] = true
	}

	// Invalid count.
	_, err = auth.GenerateRecoveryCodes(0)
	if err == nil {
		t.Error("expected error for count=0")
	}
}
