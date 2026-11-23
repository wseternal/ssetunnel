package moduleapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wseternal/ssetunnel/internal/moduleapi"
)

// TestBuildRequestPreservesRawQuery reproduces the defect this iteration fixes:
// a module invocation lost its query string because the request was rebuilt
// from the operation path alone.
func TestBuildRequestPreservesRawQuery(t *testing.T) {
	src := httptest.NewRequest(http.MethodGet, "/api/v1/modules/invoke/box/io.test/sync?from=2026-01-01&limit=50&tags=a%2Cb", nil)
	req, err := moduleapi.BuildRequest(context.Background(), src, http.MethodGet, "/sync")
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if got := req.URL.RawQuery; got != "from=2026-01-01&limit=50&tags=a%2Cb" {
		t.Fatalf("raw query = %q, want the caller's query preserved verbatim", got)
	}
	if req.URL.Path != "/sync" {
		t.Fatalf("path = %q", req.URL.Path)
	}
}

// TestBuildRequestNoQueryLeavesURLClean keeps a query-less invocation free of a
// trailing '?'.
func TestBuildRequestNoQueryLeavesURLClean(t *testing.T) {
	src := httptest.NewRequest(http.MethodGet, "/api/v1/modules/invoke/box/io.test/sync", nil)
	req, err := moduleapi.BuildRequest(context.Background(), src, http.MethodGet, "/sync")
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if req.URL.RawQuery != "" || strings.Contains(req.URL.String(), "?") {
		t.Fatalf("url = %q, want no query marker", req.URL.String())
	}
}

// TestBuildRequestPreservesEscapedPathParameter proves an encoded slash inside a
// path parameter survives as one segment.
func TestBuildRequestPreservesEscapedPathParameter(t *testing.T) {
	src := httptest.NewRequest(http.MethodGet, "/api/v1/modules/invoke/box/io.test/files/a%2Fb/raw", nil)
	escaped := "/files/a%2Fb/raw"
	req, err := moduleapi.BuildRequest(context.Background(), src, http.MethodGet, escaped)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if got := req.URL.EscapedPath(); got != escaped {
		t.Fatalf("escaped path = %q, want %q", got, escaped)
	}
}

func TestBuildRequestStripsSurfaceCredentials(t *testing.T) {
	src := httptest.NewRequest(http.MethodPost, "/api/v1/modules/invoke/box/io.test/run", strings.NewReader("{}"))
	src.Header.Set("Authorization", "Bearer surface-token")
	src.Header.Set("Cookie", "session=1")
	src.Header.Set("X-SSET-Token", "tunnel-token")
	src.Header.Set("X-SSET-Method-Override", "TRACE")
	src.Header.Set("Forwarded", "for=1.2.3.4")
	src.Header.Set("Content-Type", "application/json")
	src.Header.Set("X-Correlation-ID", "corr-test")

	req, err := moduleapi.BuildRequest(context.Background(), src, http.MethodPost, "/run")
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for _, key := range []string{"Authorization", "Cookie", "X-SSET-Token", "X-SSET-Method-Override", "Forwarded"} {
		if got := req.Header.Get(key); got != "" {
			t.Errorf("header %s leaked to the module: %q", key, got)
		}
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content type = %q, want it forwarded", got)
	}
	if got := req.Header.Get("X-Correlation-ID"); got != "corr-test" {
		t.Errorf("correlation = %q, want the caller's preserved", got)
	}
	if req.ContentLength != int64(len("{}")) {
		t.Errorf("content length = %d, want the source length", req.ContentLength)
	}
}

func TestBuildRequestStripsBrowserMetadata(t *testing.T) {
	src := httptest.NewRequest(http.MethodPost, "/api/v1/modules/invoke/box/io.test/run", strings.NewReader("{}"))
	src.Header.Set("Origin", "https://console.example")
	src.Header.Set("Referer", "https://console.example/console/modules")
	src.Header.Set("Sec-Fetch-Site", "same-origin")
	src.Header.Set("Sec-Fetch-Mode", "cors")
	src.Header.Set("Sec-Fetch-Dest", "empty")
	src.Header.Set("Sec-Fetch-User", "?1")

	req, err := moduleapi.BuildRequest(context.Background(), src, http.MethodPost, "/run")
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for _, key := range []string{"Origin", "Referer", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest", "Sec-Fetch-User"} {
		if got := req.Header.Get(key); got != "" {
			t.Errorf("header %s leaked to the module: %q", key, got)
		}
	}
}

func TestBuildRequestGeneratesCorrelationID(t *testing.T) {
	src := httptest.NewRequest(http.MethodGet, "/api/v1/modules/invoke/box/io.test/sync", nil)
	req, err := moduleapi.BuildRequest(context.Background(), src, http.MethodGet, "/sync")
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if id := req.Header.Get(moduleapi.CorrelationHeader); !strings.HasPrefix(id, "corr-") {
		t.Fatalf("correlation id = %q, want a generated id", id)
	}
}

func TestResolveMethod(t *testing.T) {
	cases := []struct {
		method, override string
		want             string
		applied          bool
	}{
		{http.MethodPost, "TRACE", http.MethodTrace, true},
		{http.MethodPost, " trace ", http.MethodTrace, true},
		{http.MethodPost, "GET", http.MethodPost, false},
		{http.MethodPost, "", http.MethodPost, false},
		{http.MethodGet, "TRACE", http.MethodGet, false},
		{http.MethodTrace, "TRACE", http.MethodTrace, false},
		{http.MethodPatch, "", http.MethodPatch, false},
	}
	for _, tc := range cases {
		got, applied := moduleapi.ResolveMethod(tc.method, tc.override)
		if got != tc.want || applied != tc.applied {
			t.Errorf("ResolveMethod(%q, %q) = (%q, %v), want (%q, %v)", tc.method, tc.override, got, applied, tc.want, tc.applied)
		}
	}
}

func TestIsPreflight(t *testing.T) {
	plain := httptest.NewRequest(http.MethodOptions, "/api/v1/modules/invoke/box/io.test/sync", nil)
	if moduleapi.IsPreflight(plain) {
		t.Fatal("OPTIONS without Access-Control-Request-Method must not be treated as a preflight")
	}
	preflight := httptest.NewRequest(http.MethodOptions, "/api/v1/modules/invoke/box/io.test/sync", nil)
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	if !moduleapi.IsPreflight(preflight) {
		t.Fatal("a real CORS preflight must be recognised")
	}
}

func TestTrailAfterSegments(t *testing.T) {
	cases := []struct {
		path, prefix string
		n            int
		want         string
		ok           bool
	}{
		{"/api/v1/modules/invoke/box/io.test/sync", "/api/v1/modules/invoke/", 2, "/sync", true},
		{"/api/v1/modules/invoke/box/io.test/sync/", "/api/v1/modules/invoke/", 2, "/sync/", true},
		{"/api/v1/modules/invoke/box/io.test/files/a%2Fb/raw", "/api/v1/modules/invoke/", 2, "/files/a%2Fb/raw", true},
		{"/api/v1/modules/invoke/box/io.test", "/api/v1/modules/invoke/", 2, "/", true},
		{"/api/v1/modules/openapi/box/io.test", "/api/v1/modules/invoke/", 2, "", false},
	}
	for _, tc := range cases {
		got, ok := moduleapi.TrailAfterSegments(tc.path, tc.prefix, tc.n)
		if got != tc.want || ok != tc.ok {
			t.Errorf("TrailAfterSegments(%q) = (%q, %v), want (%q, %v)", tc.path, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCopyResponseHeaderDropsCacheControl(t *testing.T) {
	dst := http.Header{}
	dst.Set("Cache-Control", "no-store")
	moduleapi.CopyResponseHeader(dst, "Cache-Control", []string{"public, max-age=86400"})
	if got := dst.Values("Cache-Control"); len(got) != 1 || got[0] != "no-store" {
		t.Fatalf("cache-control = %v, want the surface default preserved", got)
	}
}

func TestCopyResponseHeadersUsesAllowlist(t *testing.T) {
	dst := http.Header{}
	dst.Set("Cache-Control", "no-store")
	src := http.Header{}
	src.Set("Content-Type", "application/json")
	src.Set("Content-Disposition", `attachment; filename="a.csv"`)
	src.Set("Cache-Control", "public, max-age=86400")
	src.Set("Set-Cookie", "x=1")
	src.Set("Vary", "Origin")

	moduleapi.CopyResponseHeaders(dst, src)

	if dst.Get("Content-Type") != "application/json" {
		t.Errorf("content type = %q", dst.Get("Content-Type"))
	}
	if dst.Get("Content-Disposition") == "" {
		t.Error("content disposition must be copied")
	}
	if got := dst.Values("Cache-Control"); len(got) != 1 || got[0] != "no-store" {
		t.Errorf("cache-control = %v, want only the floor", got)
	}
	if dst.Get("Set-Cookie") != "" || dst.Get("Vary") != "" {
		t.Errorf("unexpected header leaked: %v", dst)
	}
}

func TestHeaderClassifiers(t *testing.T) {
	for _, key := range []string{"Connection", "Transfer-Encoding", "Upgrade"} {
		if !moduleapi.IsHopByHopHeader(key) {
			t.Errorf("%s must be hop-by-hop", key)
		}
	}
	for _, key := range []string{"Authorization", "Cookie", "X-SSET-Token"} {
		if !moduleapi.IsCredentialHeader(key) {
			t.Errorf("%s must be a credential", key)
		}
	}
	if moduleapi.IsCredentialHeader("X-Correlation-ID") {
		t.Error("the correlation header is not a credential")
	}
}
