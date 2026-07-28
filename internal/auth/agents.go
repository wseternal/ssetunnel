package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/lib/pq"
)

var (
	ErrAgentNotFound      = errors.New("agent not found")
	ErrDuplicateAgentID   = errors.New("agent_id already exists")
	ErrCannotDeleteDefault = errors.New("cannot delete the default agent config (NULL row)")
)

// AgentConfig represents a row in the agents table.
// When AgentID is nil, this row is the default config applied to all
// agents that don't have an explicit per-agent row.
type AgentConfig struct {
	ID              int64    `json:"id"`
	AgentID         *string  `json:"agent_id"` // nil = default row
	AllowedTargets  []string `json:"allowed_targets"`
	Description     string   `json:"description"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

// GetAgentConfig returns the config for a specific agent_id.
// If no per-agent row exists, falls back to the default (NULL) row.
func (s *Store) GetAgentConfig(ctx context.Context, agentID string) (*AgentConfig, error) {
	// Try per-agent row first
	row := s.pool.QueryRow(ctx, `
		SELECT id, agent_id, allowed_targets, description, created_at, updated_at
		FROM agents WHERE agent_id = $1`, agentID)
	cfg, err := scanAgentConfig(row)
	if err == nil {
		return cfg, nil
	}

	// Fall back to default (NULL) row
	row = s.pool.QueryRow(ctx, `
		SELECT id, agent_id, allowed_targets, description, created_at, updated_at
		FROM agents WHERE agent_id IS NULL`)
	cfg, err = scanAgentConfig(row)
	if err != nil {
		return nil, fmt.Errorf("no default agent config found: %w", err)
	}
	return cfg, nil
}

// GetDefaultAgentConfig returns the default config row (agent_id IS NULL).
func (s *Store) GetDefaultAgentConfig(ctx context.Context) (*AgentConfig, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, agent_id, allowed_targets, description, created_at, updated_at
		FROM agents WHERE agent_id IS NULL`)
	cfg, err := scanAgentConfig(row)
	if err != nil {
		return nil, fmt.Errorf("no default agent config found: %w", err)
	}
	return cfg, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanAgentConfig(row scannable) (*AgentConfig, error) {
	var cfg AgentConfig
	var targets []string
	err := row.Scan(&cfg.ID, &cfg.AgentID, pq.Array(&targets), &cfg.Description, &cfg.CreatedAt, &cfg.UpdatedAt)
	if err != nil {
		return nil, err
	}
	cfg.AllowedTargets = targets
	return &cfg, nil
}

// ListAgentConfigs returns all agent config rows, including the default row.
func (s *Store) ListAgentConfigs(ctx context.Context) ([]AgentConfig, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, agent_id, allowed_targets, description, created_at, updated_at
		FROM agents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []AgentConfig
	for rows.Next() {
		cfg, err := scanAgentConfig(rows)
		if err != nil {
			return nil, err
		}
		configs = append(configs, *cfg)
	}
	return configs, rows.Err()
}

// CreateAgentConfig creates a new per-agent config row.
func (s *Store) CreateAgentConfig(ctx context.Context, agentID, description string, allowedTargets []string) (*AgentConfig, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent_id cannot be empty")
	}
	if len(allowedTargets) == 0 {
		allowedTargets = []string{"127.0.0.1:*"}
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO agents (agent_id, allowed_targets, description)
		VALUES ($1, $2, $3)
		RETURNING id, agent_id, allowed_targets, description, created_at, updated_at`,
		agentID, pq.Array(allowedTargets), description)

	cfg, err := scanAgentConfig(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateAgentID
		}
		return nil, fmt.Errorf("create agent config: %w", err)
	}
	return cfg, nil
}

// UpdateAgentConfig updates an existing agent config by its primary key.
func (s *Store) UpdateAgentConfig(ctx context.Context, id int64, agentID *string, description *string, allowedTargets []string) (*AgentConfig, error) {
	var sets []string
	var args []any
	argIdx := 1

	if agentID != nil {
		sets = append(sets, fmt.Sprintf("agent_id = $%d", argIdx))
		args = append(args, *agentID)
		argIdx++
	}
	if description != nil {
		sets = append(sets, fmt.Sprintf("description = $%d", argIdx))
		args = append(args, *description)
		argIdx++
	}
	if allowedTargets != nil {
		sets = append(sets, fmt.Sprintf("allowed_targets = $%d", argIdx))
		args = append(args, pq.Array(allowedTargets))
		argIdx++
	}

	if len(sets) == 0 {
		// Nothing to update, just return current
		row := s.pool.QueryRow(ctx, `
			SELECT id, agent_id, allowed_targets, description, created_at, updated_at
			FROM agents WHERE id = $1`, id)
		return scanAgentConfig(row)
	}

	sets = append(sets, fmt.Sprintf("updated_at = now()"))
	query := fmt.Sprintf("UPDATE agents SET %s WHERE id = $%d RETURNING id, agent_id, allowed_targets, description, created_at, updated_at",
		strings.Join(sets, ", "), argIdx)
	args = append(args, id)

	row := s.pool.QueryRow(ctx, query, args...)
	cfg, err := scanAgentConfig(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateAgentID
		}
		return nil, fmt.Errorf("update agent config: %w", err)
	}
	return cfg, nil
}

// DeleteAgentConfig deletes a per-agent config row. The default row (NULL) cannot be deleted.
func (s *Store) DeleteAgentConfig(ctx context.Context, id int64) error {
	// Check if this is the default row
	row := s.pool.QueryRow(ctx, `SELECT agent_id FROM agents WHERE id = $1`, id)
	var agentID *string
	if err := row.Scan(&agentID); err != nil {
		return ErrAgentNotFound
	}
	if agentID == nil {
		return ErrCannotDeleteDefault
	}

	_, err := s.pool.Exec(ctx, `DELETE FROM agents WHERE id = $1`, id)
	return err
}

// TargetAllowed checks if addr matches any pattern in the allowlist.
// Patterns:
//   - "*" matches anything
//   - "host:*" matches any port on the given host
//   - "host:port" matches exact host and port
//   - "host" (no port) matches the host with any port
func TargetAllowed(patterns []string, addr string) bool {
	if len(patterns) == 0 {
		return false
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// addr might be just a host without port
		host = addr
		port = ""
	}

	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}

		// Wildcard: allow everything
		if pattern == "*" {
			return true
		}

		pHost, pPort, pErr := net.SplitHostPort(pattern)
		if pErr != nil {
			// Pattern is just a host (no port) — match host only
			pHost = pattern
			pPort = "*"
		}

		if pHost == "*" || pHost == host {
			if pPort == "*" || pPort == port {
				return true
			}
		}
	}
	return false
}
