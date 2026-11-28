package clientapp

import (
	"strings"
	"testing"
)

func TestValidateConsoleBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"valid https with prefix", "https://example.com/console", ""},
		{"valid loopback http", "http://127.0.0.1:8081", ""},
		{"userinfo", "https://user:pass@example.com/console", "credentials"},
		{"query", "https://example.com/console?x=1", "query"},
		{"fragment", "https://example.com/console#frag", "query"},
		{"force query", "https://example.com/console?", "query"},
		{"missing host", "https://", "host"},
		{"http non-local", "http://example.com", "https"},
		{"link-local", "https://169.254.169.254", "link-local"},
		{"bad scheme", "ftp://example.com", "http or https"},
		{"malformed", "://", "malformed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConsoleBaseURL(tt.raw)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateConsoleBaseURL(%q) error = %v, want nil", tt.raw, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateConsoleBaseURL(%q) error = nil, want %q", tt.raw, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateConsoleBaseURL(%q) error = %q, want %q", tt.raw, err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "user:pass") {
				t.Fatalf("validateConsoleBaseURL(%q) error leaked credentials: %q", tt.raw, err)
			}
		})
	}
}
