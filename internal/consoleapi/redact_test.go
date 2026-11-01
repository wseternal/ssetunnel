package consoleapi

import "testing"

func TestRedactSensitive(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// Bearer tokens
		{"bearer token", "Authorization: Bearer eyJhbGciOiJSUzI1NiJ9.abc123", "Authorization: [REDACTED]"},
		{"bearer with padding", "Bearer abc123def456+/==", "[REDACTED]"},
		// Key=value patterns
		{"token=", "token=abc123", "[REDACTED]"},
		{"api_key:", "api_key: sk-12345", "[REDACTED]"},
		{"api-key=", "api-key=foo-bar", "[REDACTED]"},
		{"apikey=", "apikey=mykey123", "[REDACTED]"},
		{"password=", "password=supersecret", "[REDACTED]"},
		{"secret =", "secret = mysecret", "[REDACTED]"},
		{"API_KEY uppercase", "API_KEY=FOO", "[REDACTED]"},
		{"PASSWORD uppercase", "PASSWORD=bar", "[REDACTED]"},
		// Case insensitivity
		{"mixed case bearer", "bearer ABC123", "[REDACTED]"},
		{"mixed case token", "TOKEN: xyz", "[REDACTED]"},
		// Non-matching patterns (no = or : separator)
		{"password in sentence", "password authentication enabled", "password authentication enabled"},
		{"token in sentence", "token rotation scheduled", "token rotation scheduled"},
		{"no secrets", "normal log line with no secrets", "normal log line with no secrets"},
		{"empty line", "", ""},
		// Multiple matches in one line
		{"two tokens", "Bearer abc then token=xyz", "[REDACTED] then [REDACTED]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactSensitive(tt.in)
			if got != tt.want {
				t.Errorf("redactSensitive(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
