package auth

import "testing"

func TestTargetAllowed(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		addr     string
		want     bool
	}{
		{"wildcard allows anything", []string{"*"}, "10.0.0.1:8080", true},
		{"host:* allows any port", []string{"127.0.0.1:*"}, "127.0.0.1:3000", true},
		{"host:* rejects different host", []string{"127.0.0.1:*"}, "10.0.0.1:3000", false},
		{"exact match", []string{"127.0.0.1:22"}, "127.0.0.1:22", true},
		{"exact mismatch port", []string{"127.0.0.1:22"}, "127.0.0.1:23", false},
		{"multiple patterns first match", []string{"10.0.0.1:80", "127.0.0.1:*"}, "127.0.0.1:3000", true},
		{"multiple patterns second match", []string{"10.0.0.1:80", "127.0.0.1:*"}, "10.0.0.1:80", true},
		{"multiple patterns no match", []string{"10.0.0.1:80", "127.0.0.1:22"}, "192.168.1.1:80", false},
		{"empty patterns", []string{}, "127.0.0.1:80", false},
		{"host without port in pattern matches any port", []string{"localhost"}, "localhost:8080", true},
		{"star host with star port", []string{"*:*"}, "any.host:1234", true},
		{"blank pattern ignored", []string{"", "127.0.0.1:*"}, "127.0.0.1:80", true},
		{"addr without port", []string{"myhost:*"}, "myhost", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TargetAllowed(tc.patterns, tc.addr)
			if got != tc.want {
				t.Errorf("TargetAllowed(%v, %q) = %v, want %v", tc.patterns, tc.addr, got, tc.want)
			}
		})
	}
}
