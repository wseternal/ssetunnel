// Package clientapp owns the client-side tunnel workflows shared by the
// CLI, browser, and desktop surfaces. It manages login, session resolution,
// agent setup, connect setup, and owned runtime lifecycle.
package clientapp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
)

// LoginResult contains redacted session metadata returned after a successful login.
type LoginResult struct {
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	TunnelURL string    `json:"tunnel_url"`
	ExpiresAt time.Time `json:"expires_at"`
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
func Login(p LoginParams) (*LoginResult, error) {
	consoleURL := strings.TrimRight(p.ConsoleURL, "/")
	consoleBase := consoleURL + "/api/v1"

	reqBody, _ := json.Marshal(map[string]string{
		"username":  p.Username,
		"password":  p.Password,
		"totp_code": p.TOTPCode,
	})

	resp, err := http.Post(consoleBase+"/user-login", "application/json", strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("login failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Token     string `json:"token"`
		Username  string `json:"username"`
		Role      string `json:"role"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	var expiresAt time.Time
	if result.ExpiresAt != "" {
		expiresAt, _ = time.Parse(time.RFC3339, result.ExpiresAt)
	}

	tunnelURL := p.TunnelURL
	if tunnelURL == "" {
		var derived bool
		tunnelURL, derived = DeriveTunnelURL(consoleURL)
		if !derived {
			// The tunnel URL could not be derived from the console URL.
			// The caller decides whether to warn the user.
		}
	}
	tunnelURL = strings.TrimRight(tunnelURL, "/")

	if err := auth.SaveSession(tunnelURL, result.Token, result.Username, result.Role, consoleURL, expiresAt); err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}

	return &LoginResult{
		Username:  result.Username,
		Role:      result.Role,
		TunnelURL: tunnelURL,
		ExpiresAt: expiresAt,
	}, nil
}

// CheckTOTPRequired checks whether the server requires TOTP for the given user.
func CheckTOTPRequired(consoleURL, username string) (bool, error) {
	consoleURL = strings.TrimRight(consoleURL, "/")
	consoleBase := consoleURL + "/api/v1"

	checkBody, _ := json.Marshal(map[string]string{"username": username})
	resp, err := http.Post(consoleBase+"/user-login-check", "application/json", strings.NewReader(string(checkBody)))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, nil
	}

	var check struct {
		TOTPRequired bool `json:"totp_required"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&check); err != nil {
		return false, nil
	}
	return check.TOTPRequired, nil
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
