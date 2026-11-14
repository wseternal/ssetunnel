// Package clientapp owns the client-side tunnel workflows shared by the
// CLI, browser, and desktop surfaces. It manages login, session resolution,
// agent setup, connect setup, and owned runtime lifecycle.
package clientapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
)

// ErrInvalidCredentials marks a console rejection of the username/password/
// TOTP combination. ErrConsoleUnavailable marks a network failure or a
// non-auth console error; callers map it to 502, not 401.
var (
	ErrInvalidCredentials = errors.New("invalid credentials or TOTP code")
	ErrConsoleUnavailable = errors.New("console unavailable")
)

// loginHTTPClient bounds every login/TOTP request. http.DefaultClient has
// no timeout: a black-holed console would pin the caller goroutine forever
// (the UI server's WriteTimeout kills the response, not the outbound call).
// Cross-host redirects are refused so credentials are never forwarded to a
// different origin.
var loginHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("refusing cross-host redirect to %s", req.URL.Host)
		}
		return nil
	},
}

// LoginResult contains redacted session metadata returned after a successful login.
type LoginResult struct {
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	TunnelURL string    `json:"tunnel_url"`
	ExpiresAt time.Time `json:"expires_at"`
	// Warning is set when the tunnel URL could not be derived from the
	// console URL and no explicit tunnel URL was given; the stored tunnel
	// URL is then the console URL itself, which agents may not reach.
	Warning string `json:"warning,omitempty"`
}

// LoginParams contains the parameters for a login attempt.
type LoginParams struct {
	// ConsoleURL is the tunnel server console URL (e.g. http://127.0.0.1:8081).
	ConsoleURL string
	// TunnelURL overrides the derived tunnel URL when set.
	TunnelURL string
	// Username and password for authentication.
	Username string
	Password string
	// TOTPCode is the TOTP or recovery code (may be empty).
	TOTPCode string
}

// Login authenticates against the server console API and saves the session.
func Login(ctx context.Context, p LoginParams) (*LoginResult, error) {
	consoleBase, err := consoleAPIBase(p.ConsoleURL)
	if err != nil {
		return nil, fmt.Errorf("invalid console URL: %w", err)
	}

	reqBody, _ := json.Marshal(map[string]string{
		"username":  p.Username,
		"password":  p.Password,
		"totp_code": p.TOTPCode,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, consoleBase+"/user-login", strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, fmt.Errorf("build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := loginHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConsoleUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Status only: the remote body is attacker-influenced and must not
		// reach logs or error strings (log injection, credential echo).
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("%w (status %d)", ErrInvalidCredentials, resp.StatusCode)
		}
		return nil, fmt.Errorf("%w: login failed (status %d)", ErrConsoleUnavailable, resp.StatusCode)
	}

	var result struct {
		Token     string `json:"token"`
		Username  string `json:"username"`
		Role      string `json:"role"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	var expiresAt time.Time
	if result.ExpiresAt != "" {
		expiresAt, err = time.Parse(time.RFC3339, result.ExpiresAt)
		if err != nil {
			return nil, fmt.Errorf("invalid expires_at in response: %w", err)
		}
	}

	tunnelURL := p.TunnelURL
	var warning string
	if tunnelURL == "" {
		var derived bool
		tunnelURL, derived = DeriveTunnelURL(p.ConsoleURL)
		if !derived {
			warning = fmt.Sprintf("the tunnel URL could not be derived from %s, so the session stores it as %s; if the tunnel sits behind a prefix or a different port, log in again with an explicit tunnel URL", p.ConsoleURL, tunnelURL)
		}
	}
	tunnelURL = strings.TrimRight(tunnelURL, "/")

	if err := auth.SaveSession(tunnelURL, result.Token, result.Username, result.Role, p.ConsoleURL, expiresAt); err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}

	return &LoginResult{
		Username:  result.Username,
		Role:      result.Role,
		TunnelURL: tunnelURL,
		ExpiresAt: expiresAt,
		Warning:   warning,
	}, nil
}

// CheckTOTPRequired checks whether the server requires TOTP for the given
// user. Errors are propagated: callers decide whether to fail open (CLI
// prompts for an optional code) or surface the failure (browser API).
func CheckTOTPRequired(ctx context.Context, consoleURL, username string) (bool, error) {
	consoleBase, err := consoleAPIBase(consoleURL)
	if err != nil {
		return false, fmt.Errorf("invalid console URL: %w", err)
	}

	checkBody, _ := json.Marshal(map[string]string{"username": username})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, consoleBase+"/user-login-check", strings.NewReader(string(checkBody)))
	if err != nil {
		return false, fmt.Errorf("build totp check request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := loginHTTPClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrConsoleUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%w: totp check failed (status %d)", ErrConsoleUnavailable, resp.StatusCode)
	}

	var check struct {
		TOTPRequired bool `json:"totp_required"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&check); err != nil {
		return false, fmt.Errorf("failed to parse totp check response: %w", err)
	}
	return check.TOTPRequired, nil
}

// consoleAPIBase builds the /api/v1 base URL from a console URL,
// preserving any path prefix (e.g. /console when behind a reverse proxy).
// The URL is validated with the same SSRF rules as session refresh.
func consoleAPIBase(consoleURL string) (string, error) {
	consoleURL = strings.TrimRight(consoleURL, "/")
	if err := auth.ValidateConsoleURL(consoleURL); err != nil {
		return "", err
	}
	u, err := url.Parse(consoleURL)
	if err != nil {
		return "", err
	}
	u.Path = path.Join(u.Path, "/api/v1")
	return u.String(), nil
}

// DeriveTunnelURL maps a console URL on the default port 8081 to the tunnel
// URL on 8080. Returns false when the port swap is not the whole mapping.
func DeriveTunnelURL(consoleURL string) (string, bool) {
	u, err := url.Parse(consoleURL)
	if err != nil {
		return consoleURL, false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return consoleURL, false
	}
	if u.Hostname() == "" || u.Port() != "8081" {
		return consoleURL, false
	}
	if strings.TrimRight(u.Path, "/") != "" {
		return consoleURL, false
	}
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	u.Host = host + ":8080"
	return u.String(), true
}