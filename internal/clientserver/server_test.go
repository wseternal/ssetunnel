package clientserver

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	clientassets "github.com/wseternal/ssetunnel/frontend/client"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Config{})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(func() { s.listener.Close() })
	return s
}

func firstAsset(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(clientassets.ClientWebRootFs, "assets/*.js")
	if err != nil || len(matches) == 0 {
		t.Fatalf("no embedded JS asset found: %v", err)
	}
	return "/" + matches[0]
}

func assertContractHeaders(t *testing.T, h http.Header, wantCache string) {
	t.Helper()
	checks := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          wantCache,
	}
	for k, want := range checks {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("Content-Security-Policy = %q, want default-src 'self'", csp)
	}
}

// TestSecurityHeadersOnAllResponseClasses pins W7: the contract headers must
// cover the SPA document, assets, API errors, and mux-generated responses —
// not just the authenticated API happy path.
func TestSecurityHeadersOnAllResponseClasses(t *testing.T) {
	s := newTestServer(t)
	asset := firstAsset(t)

	tests := []struct {
		name      string
		method    string
		path      string
		secret    bool
		wantCode  int
		wantCache string
	}{
		{"index", "GET", "/", false, http.StatusOK, "no-store"},
		{"asset", "GET", asset, false, http.StatusOK, "public, max-age=31536000, immutable"},
		{"unknown api", "GET", "/api/nope", true, http.StatusNotFound, "no-store"},
		{"method not allowed", "POST", "/", false, http.StatusMethodNotAllowed, "no-store"},
		{"unauthorized api", "GET", "/api/agent/status", false, http.StatusUnauthorized, "no-store"},
		{"bare assets", "GET", "/assets", false, http.StatusNotFound, "no-store"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Host = "127.0.0.1:1234"
			if tt.secret {
				req.Header.Set("Authorization", "Bearer "+s.secret)
			}
			rec := httptest.NewRecorder()
			s.handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			assertContractHeaders(t, rec.Header(), tt.wantCache)
		})
	}
}

func TestAssetETagRevalidation(t *testing.T) {
	s := newTestServer(t)
	asset := firstAsset(t)

	req := httptest.NewRequest("GET", asset, nil)
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("asset response has no ETag")
	}

	req = httptest.NewRequest("GET", asset, nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("revalidation status = %d, want 304", rec.Code)
	}
}

// TestHostCheck pins W9: the bracketed IPv6 Host form must parse, and
// non-loopback hosts must be rejected before auth.
func TestHostCheck(t *testing.T) {
	s := newTestServer(t)
	tests := []struct {
		host     string
		wantCode int
	}{
		{"evil.com", http.StatusForbidden},
		{"evil.com:8080", http.StatusForbidden},
		{"127.0.0.1:8080", http.StatusUnauthorized},
		{"localhost:8080", http.StatusUnauthorized},
		{"[::1]:8080", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/agent/status", nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			s.handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("Host %q: status = %d, want %d", tt.host, rec.Code, tt.wantCode)
			}
		})
	}
}

// TestOriginCheck pins W9: Origin must match scheme, loopback hostname, and
// port. Cross-port and cross-scheme origins are rejected; the loopback host
// set keeps localhost↔127.0.0.1 usable.
func TestOriginCheck(t *testing.T) {
	s := newTestServer(t)
	tests := []struct {
		origin   string
		wantCode int
	}{
		{"http://evil.com", http.StatusForbidden},
		{"http://127.0.0.1:9999", http.StatusForbidden},
		{"https://127.0.0.1:8080", http.StatusForbidden},
		{"http://localhost:8080", http.StatusUnauthorized},
		{"http://127.0.0.1:8080", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.origin, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/agent/status", nil)
			req.Host = "127.0.0.1:8080"
			req.Header.Set("Origin", tt.origin)
			rec := httptest.NewRecorder()
			s.handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("Origin %q: status = %d, want %d", tt.origin, rec.Code, tt.wantCode)
			}
		})
	}
}

// TestLoopbackListenValidation pins W8: wildcard and non-loopback binds are
// rejected before any listener opens.
func TestLoopbackListenValidation(t *testing.T) {
	tests := []struct {
		listen  string
		wantErr bool
	}{
		{"127.0.0.1:0", false},
		{"localhost:0", false},
		{"[::1]:0", false},
		{"0.0.0.0:0", true},
		{":0", true},
		{"192.168.1.1:0", true},
		{"[2001:db8::1]:0", true},
	}
	for _, tt := range tests {
		t.Run(tt.listen, func(t *testing.T) {
			err := validateLoopbackListen(tt.listen)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateLoopbackListen(%q) err = %v, wantErr %v", tt.listen, err, tt.wantErr)
			}
		})
	}
}

func TestSecretComparison(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/agent/status", nil)
	req.Host = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer wrong-secret")
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: status = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/agent/status", nil)
	req.Host = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+s.secret)
	rec = httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("correct secret: status = %d, want 200", rec.Code)
	}
}