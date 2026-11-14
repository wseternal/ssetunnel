package clientapp

import (
	"errors"
	"testing"
)

func TestConsoleAPIBase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{"root", "http://127.0.0.1:8081", "http://127.0.0.1:8081/api/v1", nil},
		{"trailing slash", "http://127.0.0.1:8081/", "http://127.0.0.1:8081/api/v1", nil},
		{"console prefix preserved", "https://ex.com/console", "https://ex.com/console/api/v1", nil},
		{"non-loopback http rejected", "http://192.168.1.5:8081", "", ErrInvalidConsoleURL},
		{"non-http scheme rejected", "ftp://ex.com", "", ErrInvalidConsoleURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := consoleAPIBase(tt.input)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("consoleAPIBase(%q) err = %v, want %v", tt.input, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("consoleAPIBase(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("consoleAPIBase(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDeriveTunnelURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		input       string
		expect      string
		expectFound bool
	}{
		{"default ports", "http://127.0.0.1:8081", "http://127.0.0.1:8080", true},
		{"https default ports", "https://tunnel.example.com:8081", "https://tunnel.example.com:8080", true},
		{"root path", "http://127.0.0.1:8081/", "http://127.0.0.1:8080/", true},
		{"ipv6 loopback", "http://[::1]:8081", "http://[::1]:8080", true},
		// An IPv6 address can itself contain ":8081". A string replace on Host
		// rewrote the address and left the port alone while reporting success.
		{"ipv6 global containing 8081", "http://[2001:db8:8081::1]:8081", "http://[2001:db8:8081::1]:8080", true},
		{"other port not derived", "http://host:9081", "http://host:9081", false},
		{"port starting with 8081 not derived", "http://ex.com:80810", "http://ex.com:80810", false},
		// No port at all is the documented proxy layout: the console is at the
		// root and the tunnel lives under a prefix that cannot be derived, so
		// login must warn instead of quietly saving the console URL as a tunnel.
		{"no port not derived", "http://host", "http://host", false},
		{"proxy root not derived", "https://tunnel.example.com", "https://tunnel.example.com", false},
		{"console behind a prefix not derived", "https://ex.com:8081/console", "https://ex.com:8081/console", false},
		{"query not derived", "https://ex.com:8081/p?x=y", "https://ex.com:8081/p?x=y", false},
		{"8081 in a query not derived", "https://ex.com/?b=http://y:8081", "https://ex.com/?b=http://y:8081", false},
		{"unparseable not derived", "ht tp://bad", "ht tp://bad", false},
		// net/url happily parses things that cannot be dialled as an HTTP base.
		// Deriving "successfully" from one of those stores a URL the agent can
		// never reach, which is worse than the honest no-derivation warning.
		{"scheme-relative not derived", "//host:8081", "//host:8081", false},
		{"empty host not derived", "http://:8081", "http://:8081", false},
		{"bare host:port parsed as a scheme", "127.0.0.1:8081", "127.0.0.1:8081", false},
		{"non-http scheme not derived", "ws://host:8081", "ws://host:8081", false},
		// Credentials in the console URL survive the rewrite; only the port moves.
		{"userinfo preserved", "http://u:p@h:8081", "http://u:p@h:8080", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, derived := DeriveTunnelURL(tt.input)
			if got != tt.expect {
				t.Errorf("DeriveTunnelURL(%q) = %q, want %q", tt.input, got, tt.expect)
			}
			if derived != tt.expectFound {
				t.Errorf("DeriveTunnelURL(%q) derived = %v, want %v", tt.input, derived, tt.expectFound)
			}
		})
	}
}
