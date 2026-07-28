package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalidToken   = errors.New("invalid or revoked token")
	ErrInvalidPIN     = errors.New("invalid, expired, or already used PIN")
	ErrInvalidSession = errors.New("invalid or expired admin session")
)

type TokenInfo struct {
	ID          int64      `json:"id"`
	Role        string     `json:"role"`
	Description string     `json:"description,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

type Store struct {
	pool  *pgxpool.Pool
	cache sync.Map // map[digest]TokenInfo
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{
		pool: pool,
	}
}

func (s *Store) CreatePIN(ctx context.Context, rawPIN, role string, ttl time.Duration) error {
	digest := ComputeDigest(rawPIN)
	expiresAt := time.Now().UTC().Add(ttl)

	query := `INSERT INTO pins (digest, role, expires_at) VALUES ($1, $2, $3)`
	_, err := s.pool.Exec(ctx, query, digest, role, expiresAt)
	if err != nil {
		return fmt.Errorf("failed to insert pin: %w", err)
	}
	return nil
}

func (s *Store) VerifyAndUsePIN(ctx context.Context, rawPIN string) (string, error) {
	digest := ComputeDigest(rawPIN)

	query := `
		UPDATE pins
		SET used_at = CURRENT_TIMESTAMP
		WHERE digest = $1 AND used_at IS NULL AND expires_at > CURRENT_TIMESTAMP
		RETURNING role
	`

	var role string
	err := s.pool.QueryRow(ctx, query, digest).Scan(&role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrInvalidPIN
		}
		return "", fmt.Errorf("failed to verify pin: %w", err)
	}

	return role, nil
}

func (s *Store) CreateToken(ctx context.Context, rawToken, role, description string, expiresAt *time.Time) error {
	digest := ComputeDigest(rawToken)

	query := `INSERT INTO tokens (digest, role, description, expires_at) VALUES ($1, $2, $3, $4)`
	_, err := s.pool.Exec(ctx, query, digest, role, description, expiresAt)
	if err != nil {
		return fmt.Errorf("failed to insert token: %w", err)
	}
	return nil
}

func (s *Store) ValidateToken(ctx context.Context, rawToken string) (TokenInfo, error) {
	digest := ComputeDigest(rawToken)

	// Read-through cache check
	if cached, ok := s.cache.Load(digest); ok {
		info := cached.(TokenInfo)
		if info.RevokedAt != nil {
			return TokenInfo{}, ErrInvalidToken
		}
		if info.ExpiresAt != nil && info.ExpiresAt.Before(time.Now().UTC()) {
			return TokenInfo{}, ErrInvalidToken
		}
		return info, nil
	}

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

	// Store in cache
	s.cache.Store(digest, info)
	return info, nil
}

func (s *Store) RevokeToken(ctx context.Context, rawToken string) error {
	digest := ComputeDigest(rawToken)

	query := `UPDATE tokens SET revoked_at = CURRENT_TIMESTAMP WHERE digest = $1`
	_, err := s.pool.Exec(ctx, query, digest)
	if err != nil {
		return fmt.Errorf("failed to revoke token: %w", err)
	}

	// Evict from cache
	s.cache.Delete(digest)
	return nil
}

func (s *Store) RevokeTokenByID(ctx context.Context, id int64) error {
	query := `UPDATE tokens SET revoked_at = CURRENT_TIMESTAMP WHERE id = $1 RETURNING digest`
	var digest string
	err := s.pool.QueryRow(ctx, query, id).Scan(&digest)
	if err != nil {
		return fmt.Errorf("failed to revoke token by ID: %w", err)
	}
	s.cache.Delete(digest)
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

func (s *Store) CreateAdminSession(ctx context.Context, rawSessionToken string, ttl time.Duration) error {
	digest := ComputeDigest(rawSessionToken)
	expiresAt := time.Now().UTC().Add(ttl)

	query := `INSERT INTO admin_sessions (digest, expires_at) VALUES ($1, $2)`
	_, err := s.pool.Exec(ctx, query, digest, expiresAt)
	if err != nil {
		return fmt.Errorf("failed to create admin session: %w", err)
	}
	return nil
}

func (s *Store) ValidateAdminSession(ctx context.Context, rawSessionToken string) error {
	digest := ComputeDigest(rawSessionToken)

	query := `
		SELECT expires_at
		FROM admin_sessions
		WHERE digest = $1 AND expires_at > CURRENT_TIMESTAMP
	`

	var expiresAt time.Time
	err := s.pool.QueryRow(ctx, query, digest).Scan(&expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidSession
		}
		return fmt.Errorf("failed to validate admin session: %w", err)
	}

	return nil
}
