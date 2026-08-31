package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalidToken   = errors.New("invalid or revoked token")
	ErrInvalidSession = errors.New("invalid or expired session")
	ErrUserNotFound   = errors.New("user not found")
	ErrUserDisabled   = errors.New("user account is disabled")
	ErrDuplicateUser  = errors.New("username already exists")
)

type TokenInfo struct {
	ID          int64      `json:"id"`
	Role        string     `json:"role"`
	Description string     `json:"description,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	UserID      *int64     `json:"user_id,omitempty"`
}

type UserInfo struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	Role        string     `json:"role"`
	PermConnect bool       `json:"perm_connect"`
	PermAgent   bool       `json:"perm_agent"`
	TOTPSecret  string     `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	DisabledAt  *time.Time `json:"disabled_at,omitempty"`
}

type UserSessionInfo struct {
	UserID      int64     `json:"user_id"`
	Role        string    `json:"role"`
	PermConnect bool      `json:"perm_connect"`
	PermAgent   bool      `json:"perm_agent"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type Store struct {
	pool   *pgxpool.Pool
	pepper string // HMAC key for recovery code digests (empty = SHA-256 fallback)
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{
		pool: pool,
	}
}

// SetRecoveryCodePepper sets the HMAC key used for recovery code digests.
// When set, recovery codes are stored as HMAC-SHA256 instead of plain SHA-256,
// making offline brute-force attacks infeasible.
func (s *Store) SetRecoveryCodePepper(pepper string) {
	s.pepper = pepper
}

// RecoveryCodeDigest computes the digest for a recovery code using HMAC if a pepper
// is configured, or SHA-256 as a fallback.
func (s *Store) RecoveryCodeDigest(plaintextCode string) string {
	if s.pepper != "" {
		return ComputeHMACDigest(plaintextCode, s.pepper)
	}
	return ComputeDigest(plaintextCode)
}

func (s *Store) CreateToken(ctx context.Context, rawToken, role, description string, expiresAt *time.Time) error {
	return s.createTokenTx(ctx, s.pool, rawToken, role, description, expiresAt)
}

// createTokenTx inserts a token using the given queryable (pool or tx).
func (s *Store) createTokenTx(ctx context.Context, q interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, rawToken, role, description string, expiresAt *time.Time) error {
	digest := ComputeDigest(rawToken)

	query := `INSERT INTO tokens (digest, role, description, expires_at) VALUES ($1, $2, $3, $4)`
	_, err := q.Exec(ctx, query, digest, role, description, expiresAt)
	if err != nil {
		return fmt.Errorf("failed to insert token: %w", err)
	}
	return nil
}

func (s *Store) ValidateToken(ctx context.Context, rawToken string) (TokenInfo, error) {
	digest := ComputeDigest(rawToken)

	query := `
		SELECT id, role, description, created_at, expires_at, revoked_at
		FROM tokens
		WHERE digest = $1
	`

	var info TokenInfo
	var desc *string
	err := s.pool.QueryRow(ctx, query, digest).Scan(
		&info.ID,
		&info.Role,
		&desc,
		&info.CreatedAt,
		&info.ExpiresAt,
		&info.RevokedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TokenInfo{}, ErrInvalidToken
		}
		return TokenInfo{}, fmt.Errorf("failed to validate token: %w", err)
	}

	if desc != nil {
		info.Description = *desc
	}

	if info.RevokedAt != nil || (info.ExpiresAt != nil && info.ExpiresAt.Before(time.Now().UTC())) {
		return TokenInfo{}, ErrInvalidToken
	}

	return info, nil
}

func (s *Store) RevokeToken(ctx context.Context, rawToken string) error {
	digest := ComputeDigest(rawToken)

	query := `UPDATE tokens SET revoked_at = CURRENT_TIMESTAMP WHERE digest = $1`
	_, err := s.pool.Exec(ctx, query, digest)
	if err != nil {
		return fmt.Errorf("failed to revoke token: %w", err)
	}
	return nil
}

func (s *Store) RevokeTokenByID(ctx context.Context, id int64) error {
	query := `UPDATE tokens SET revoked_at = CURRENT_TIMESTAMP WHERE id = $1`
	_, err := s.pool.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to revoke token by ID: %w", err)
	}
	return nil
}

func (s *Store) ListTokens(ctx context.Context) ([]TokenInfo, error) {
	query := `
		SELECT id, role, description, created_at, expires_at, revoked_at
		FROM tokens
		ORDER BY created_at DESC
	`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list tokens: %w", err)
	}
	defer rows.Close()

	var tokens []TokenInfo
	for rows.Next() {
		var info TokenInfo
		var desc *string
		if err := rows.Scan(&info.ID, &info.Role, &desc, &info.CreatedAt, &info.ExpiresAt, &info.RevokedAt); err != nil {
			return nil, fmt.Errorf("failed to scan token row: %w", err)
		}
		if desc != nil {
			info.Description = *desc
		}
		tokens = append(tokens, info)
	}

	return tokens, nil
}

// --- User CRUD ---

func (s *Store) CreateUser(ctx context.Context, username, passwordHash, role string, permConnect, permAgent bool) (*UserInfo, error) {
	query := `
		INSERT INTO users (username, password_hash, role, perm_connect, perm_agent)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at
	`
	u := &UserInfo{Username: username, Role: role, PermConnect: permConnect, PermAgent: permAgent}
	err := s.pool.QueryRow(ctx, query, username, passwordHash, role, permConnect, permAgent).Scan(&u.ID, &u.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateUser
		}
		return nil, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*UserInfo, error) {
	query := `SELECT id, username, password_hash, role, perm_connect, perm_agent, totp_secret, created_at, disabled_at FROM users WHERE username = $1`
	var u UserInfo
	var pwHash string
	err := s.pool.QueryRow(ctx, query, username).Scan(
		&u.ID, &u.Username, &pwHash, &u.Role, &u.PermConnect, &u.PermAgent, &u.TOTPSecret, &u.CreatedAt, &u.DisabledAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("get user by username: %w", err)
	}
	return &u, nil
}

// getUserPasswordHash returns the password hash for a user (used by ValidatePassword).
func (s *Store) getUserPasswordHash(ctx context.Context, username string) (string, error) {
	query := `SELECT password_hash FROM users WHERE username = $1 AND disabled_at IS NULL`
	var hash string
	err := s.pool.QueryRow(ctx, query, username).Scan(&hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrUserNotFound
		}
		return "", fmt.Errorf("get password hash: %w", err)
	}
	return hash, nil
}

// ValidatePassword looks up the user by username and checks the password.
// Returns the UserInfo on success.
func (s *Store) ValidatePassword(ctx context.Context, username, password string) (*UserInfo, error) {
	hash, err := s.getUserPasswordHash(ctx, username)
	if err != nil {
		return nil, err
	}
	if err := CheckPassword(hash, password); err != nil {
		return nil, ErrUserNotFound // same error to avoid user enumeration
	}
	return s.GetUserByUsername(ctx, username)
}

func (s *Store) ListUsers(ctx context.Context) ([]UserInfo, error) {
	query := `SELECT id, username, role, perm_connect, perm_agent, created_at, disabled_at FROM users ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []UserInfo
	for rows.Next() {
		var u UserInfo
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.PermConnect, &u.PermAgent, &u.CreatedAt, &u.DisabledAt); err != nil {
			return nil, fmt.Errorf("scan user row: %w", err)
		}
		users = append(users, u)
	}
	return users, nil
}

func (s *Store) UpdateUserWithDisabled(ctx context.Context, id int64, role *string, passwordHash *string, permConnect *bool, permAgent *bool, disabled *bool) error {
	// Validate role if provided.
	if role != nil && *role != "admin" && *role != "user" && *role != "agent" {
		return fmt.Errorf("invalid role: %q", *role)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Build and execute the field update.
	setClauses := make([]string, 0, 5)
	args := make([]interface{}, 0, 5)
	n := 0
	if role != nil {
		n++
		setClauses = append(setClauses, fmt.Sprintf("role = $%d", n))
		args = append(args, *role)
	}
	if passwordHash != nil {
		n++
		setClauses = append(setClauses, fmt.Sprintf("password_hash = $%d", n))
		args = append(args, *passwordHash)
	}
	if permConnect != nil {
		n++
		setClauses = append(setClauses, fmt.Sprintf("perm_connect = $%d", n))
		args = append(args, *permConnect)
	}
	if permAgent != nil {
		n++
		setClauses = append(setClauses, fmt.Sprintf("perm_agent = $%d", n))
		args = append(args, *permAgent)
	}
	if disabled != nil {
		n++
		if *disabled {
			setClauses = append(setClauses, fmt.Sprintf("disabled_at = $%d", n))
			args = append(args, time.Now().UTC())
		} else {
			setClauses = append(setClauses, "disabled_at = NULL")
		}
	}

	if len(setClauses) > 0 {
		n++
		query := fmt.Sprintf("UPDATE users SET %s WHERE id = $%d", strings.Join(setClauses, ", "), n)
		args = append(args, id)
		tag, err := tx.Exec(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("update user: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrUserNotFound
		}
	}

	return tx.Commit(ctx)
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Delete dependent sessions first to avoid FK violation.
	if _, err := tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	// Delete dependent tokens.
	if _, err := tx.Exec(ctx, `DELETE FROM tokens WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("delete user tokens: %w", err)
	}

	tag, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return tx.Commit(ctx)
}

// EnsureAdminUser checks whether any admin users exist. If none do, it creates
// one with username "admin" and a cryptographically random password, returning
// the plaintext password so the operator can log it. Returns ("" , nil) when an
// admin already exists. Uses INSERT ... ON CONFLICT DO NOTHING to handle
// concurrent startup races gracefully.
func (s *Store) EnsureAdminUser(ctx context.Context) (string, error) {
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&count); err != nil {
		return "", fmt.Errorf("count admin users: %w", err)
	}
	if count > 0 {
		return "", nil
	}

	// Generate a random 16-byte password, base64url-encoded (22 chars).
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate random password: %w", err)
	}
	plaintext := base64.RawURLEncoding.EncodeToString(raw)

	hash, err := HashPassword(plaintext)
	if err != nil {
		return "", fmt.Errorf("hash admin password: %w", err)
	}

	_, err = s.CreateUser(ctx, "admin", hash, "admin", true, true)
	if err != nil {
		if errors.Is(err, ErrDuplicateUser) {
			// Another instance created the admin user concurrently.
			return "", nil
		}
		return "", fmt.Errorf("seed admin user: %w", err)
	}

	return plaintext, nil
}

// --- User Sessions ---

// CreateUserSession creates a new user session with a generated token.
// Returns the computed expiry time so callers can use the same value
// in responses without clock skew.
func (s *Store) CreateUserSession(ctx context.Context, userID int64, rawToken string, ttl time.Duration) (time.Time, error) {
	digest := ComputeDigest(rawToken)
	expiresAt := time.Now().UTC().Add(ttl)

	query := `INSERT INTO user_sessions (user_id, digest, expires_at) VALUES ($1, $2, $3)`
	_, err := s.pool.Exec(ctx, query, userID, digest, expiresAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("create user session: %w", err)
	}
	return expiresAt, nil
}

func (s *Store) ValidateUserSession(ctx context.Context, rawToken string) (*UserSessionInfo, error) {
	digest := ComputeDigest(rawToken)

	query := `
		SELECT us.user_id, us.expires_at, u.role, u.perm_connect, u.perm_agent
		FROM user_sessions us
		JOIN users u ON u.id = us.user_id
		WHERE us.digest = $1 AND us.expires_at > CURRENT_TIMESTAMP AND u.disabled_at IS NULL
	`

	var info UserSessionInfo
	err := s.pool.QueryRow(ctx, query, digest).Scan(&info.UserID, &info.ExpiresAt, &info.Role, &info.PermConnect, &info.PermAgent)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidSession
		}
		return nil, fmt.Errorf("validate user session: %w", err)
	}
	return &info, nil
}

// RevokeUserSession deletes a user session by its raw token, invalidating it immediately.
func (s *Store) RevokeUserSession(ctx context.Context, rawToken string) error {
	digest := ComputeDigest(rawToken)
	_, err := s.pool.Exec(ctx, `DELETE FROM user_sessions WHERE digest = $1`, digest)
	return err
}

// RefreshResult holds the output of a successful session refresh.
type RefreshResult struct {
	Token     string    // new raw session token
	ExpiresAt time.Time // new session expiry
	Info      *UserSessionInfo
}

// RefreshUserSession atomically rotates a user session token: validates the
// current token, creates a new session with a fresh TTL, and deletes the old
// session — all in a single transaction. Returns the new token and session info.
func (s *Store) RefreshUserSession(ctx context.Context, rawToken string, ttl time.Duration) (*RefreshResult, error) {
	oldDigest := ComputeDigest(rawToken)

	// Generate new token before opening the transaction to minimize FOR UPDATE
	// lock hold time (crypto/rand can block on entropy-starved systems).
	newToken, err := GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("generate refresh token: %w", err)
	}
	newDigest := ComputeDigest(newToken)
	expiresAt := time.Now().UTC().Add(ttl)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin refresh tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Validate current token and fetch user info within the transaction.
	var info UserSessionInfo
	validateQuery := `
		SELECT us.user_id, us.expires_at, u.role, u.perm_connect, u.perm_agent
		FROM user_sessions us
		JOIN users u ON u.id = us.user_id
		WHERE us.digest = $1 AND us.expires_at > CURRENT_TIMESTAMP AND u.disabled_at IS NULL
		FOR UPDATE
	`
	if err := tx.QueryRow(ctx, validateQuery, oldDigest).Scan(
		&info.UserID, &info.ExpiresAt, &info.Role, &info.PermConnect, &info.PermAgent,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidSession
		}
		return nil, fmt.Errorf("validate session for refresh: %w", err)
	}

	// Create new session.
	if _, err := tx.Exec(ctx,
		`INSERT INTO user_sessions (user_id, digest, expires_at) VALUES ($1, $2, $3)`,
		info.UserID, newDigest, expiresAt,
	); err != nil {
		return nil, fmt.Errorf("create refreshed session: %w", err)
	}

	// Delete old session.
	if _, err := tx.Exec(ctx, `DELETE FROM user_sessions WHERE digest = $1`, oldDigest); err != nil {
		return nil, fmt.Errorf("delete old session: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit refresh: %w", err)
	}

	info.ExpiresAt = expiresAt
	return &RefreshResult{
		Token:     newToken,
		ExpiresAt: expiresAt,
		Info:      &info,
	}, nil
}

// --- TOTP & Recovery Codes ---

// UserTOTPEnrolled reports whether the named user has a non-empty TOTP secret.
// Returns (enrolled, found, err) — found is false when the user does not exist,
// allowing callers to handle anti-enumeration in constant time (single query).
func (s *Store) UserTOTPEnrolled(ctx context.Context, username string) (enrolled, found bool, err error) {
	var val *bool
	err = s.pool.QueryRow(ctx,
		`SELECT CASE WHEN EXISTS(SELECT 1 FROM users WHERE username = $1)
		  THEN (SELECT totp_secret != '' FROM users WHERE username = $1)
		  ELSE NULL END`, username,
	).Scan(&val)
	if err != nil {
		return false, false, fmt.Errorf("user totp enrolled: %w", err)
	}
	if val == nil {
		return false, false, nil // user not found
	}
	return *val, true, nil
}

// GetUserByID returns the UserInfo for a given user ID.
func (s *Store) GetUserByID(ctx context.Context, id int64) (*UserInfo, error) {
	query := `SELECT id, username, role, perm_connect, perm_agent, totp_secret, created_at, disabled_at FROM users WHERE id = $1`
	var u UserInfo
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&u.ID, &u.Username, &u.Role, &u.PermConnect, &u.PermAgent, &u.TOTPSecret, &u.CreatedAt, &u.DisabledAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return &u, nil
}

// SetTOTPSecret updates the TOTP secret for a user. Pass "" to clear TOTP enrollment.
func (s *Store) SetTOTPSecret(ctx context.Context, userID int64, secret string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET totp_secret = $1 WHERE id = $2`, secret, userID)
	if err != nil {
		return fmt.Errorf("set totp secret: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

// SaveRecoveryCodes replaces all unused recovery codes for a user with new ones.
// The digests slice should contain digests (HMAC-SHA256 or SHA-256) of the plaintext codes.
func (s *Store) SaveRecoveryCodes(ctx context.Context, userID int64, digests []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Delete existing unused codes.
	if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1 AND used_at IS NULL`, userID); err != nil {
		return fmt.Errorf("delete old recovery codes: %w", err)
	}

	// Insert new codes.
	for _, d := range digests {
		if _, err := tx.Exec(ctx, `INSERT INTO recovery_codes (user_id, code_digest) VALUES ($1, $2)`, userID, d); err != nil {
			return fmt.Errorf("insert recovery code: %w", err)
		}
	}

	return tx.Commit(ctx)
}

// ConsumeRecoveryCode atomically marks a recovery code as used for the given user.
// Returns true if a matching unused code was found and consumed.
func (s *Store) ConsumeRecoveryCode(ctx context.Context, userID int64, plaintextCode string) (bool, error) {
	digest := s.RecoveryCodeDigest(plaintextCode)
	var id int64
	err := s.pool.QueryRow(ctx,
		`UPDATE recovery_codes SET used_at = CURRENT_TIMESTAMP
		 WHERE user_id = $1 AND code_digest = $2 AND used_at IS NULL
		 RETURNING id`, userID, digest,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("consume recovery code: %w", err)
	}
	return true, nil
}

// CountUnusedRecoveryCodes returns the number of unused recovery codes for a user.
func (s *Store) CountUnusedRecoveryCodes(ctx context.Context, userID int64) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM recovery_codes WHERE user_id = $1 AND used_at IS NULL`, userID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count recovery codes: %w", err)
	}
	return count, nil
}

// DeleteRecoveryCodes removes all recovery codes (used and unused) for a user.
func (s *Store) DeleteRecoveryCodes(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("delete recovery codes: %w", err)
	}
	return nil
}

// SetTOTPSecretAndRecoveryCodes atomically sets the TOTP secret and saves recovery codes
// in a single transaction. If either operation fails, both are rolled back.
func (s *Store) SetTOTPSecretAndRecoveryCodes(ctx context.Context, userID int64, secret string, digests []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Save recovery codes first (so failure leaves user unenrolled, not half-enrolled).
	if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1 AND used_at IS NULL`, userID); err != nil {
		return fmt.Errorf("delete old recovery codes: %w", err)
	}
	for _, d := range digests {
		if _, err := tx.Exec(ctx, `INSERT INTO recovery_codes (user_id, code_digest) VALUES ($1, $2)`, userID, d); err != nil {
			return fmt.Errorf("insert recovery code: %w", err)
		}
	}

	// Set TOTP secret last.
	tag, err := tx.Exec(ctx, `UPDATE users SET totp_secret = $1 WHERE id = $2`, secret, userID)
	if err != nil {
		return fmt.Errorf("set totp secret: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}

	return tx.Commit(ctx)
}

// AnyTOTPEnrolled reports whether any user in the system has TOTP configured.
func (s *Store) AnyTOTPEnrolled(ctx context.Context) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE totp_secret != '')`,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("any totp enrolled: %w", err)
	}
	return exists, nil
}

// ClearTOTPAndRecoveryCodes atomically clears a user's TOTP secret and recovery codes
// in a single transaction, preventing orphaned recovery codes on partial failure.
func (s *Store) ClearTOTPAndRecoveryCodes(ctx context.Context, userID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1 AND used_at IS NULL`, userID); err != nil {
		return fmt.Errorf("delete recovery codes: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE users SET totp_secret = '' WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("clear totp secret: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}

	return tx.Commit(ctx)
}

// isUniqueViolation checks if an error is a PostgreSQL unique constraint violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return err != nil && strings.Contains(err.Error(), "duplicate key")
}
