package consoleserver_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/frontend"
	"github.com/wseternal/ssetunnel/internal/consoleserver"
	"github.com/wseternal/ssetunnel/internal/server"
)

// firstAssetJS returns the filename of a real built JS asset inside the embedded
// console FS, so the test does not hardcode a content hash that changes on rebuild.
func firstAssetJS(t *testing.T) string {
	t.Helper()
	var found string
	err := fs.WalkDir(frontend.ConsoleWebRootFs, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".js") {
			found = path.Base(p)
			return fs.SkipAll
		}
		return nil
	})
	if err != nil || found == "" {
		t.Fatalf("no .js asset embedded under assets/: run `task fe:build` (err=%v)", err)
	}
	return found
}

// TestConsoleRoutes covers the composed console router's route classes and the
// headers each must carry. It exists because the /assets/ handler added in 23908c0
// opened the embedded FS with a leading-slash path — invalid for fs.Sub's Open,
// which gates on fs.ValidPath — so every hashed asset 404'd and the SPA never
// booted, while build, vet and the whole Go suite stayed green.
func TestConsoleRoutes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// No DB and no tunnel server: the SPA and asset routes must not need either.
	h := consoleserver.NewConsoleHandler(ctx, nil, nil, nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	asset := firstAssetJS(t)

	do := func(method, p string, hdr map[string]string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, ts.URL+p, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, p, err)
		}
		return res
	}

	t.Run("hashed asset is served with immutable cache and real MIME", func(t *testing.T) {
		res := do(http.MethodGet, "/assets/"+asset, nil)
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /assets/%s = %d, want 200 — asset handler cannot open the embedded FS; the console renders blank", asset, res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
			t.Errorf("Content-Type = %q, want text/javascript", ct)
		}
		if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
			t.Errorf("Cache-Control = %q, want immutable caching for a content-hashed file", cc)
		}
	})

	t.Run("asset responses keep the console security headers", func(t *testing.T) {
		res := do(http.MethodGet, "/assets/"+asset, nil)
		defer res.Body.Close()
		for hdr, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "SAMEORIGIN",
			"Referrer-Policy":        "origin-when-cross-origin",
		} {
			if got := res.Header.Get(hdr); got != want {
				t.Errorf("%s = %q, want %q — bypassing litespaserver.setBaseHeaders must not drop it", hdr, got, want)
			}
		}
	})

	t.Run("asset supports HEAD and answers If-None-Match with 304", func(t *testing.T) {
		res := do(http.MethodHead, "/assets/"+asset, nil)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("HEAD /assets/%s = %d, want 200 — health probes and curl -I must work", asset, res.StatusCode)
		}

		res = do(http.MethodGet, "/assets/"+asset, nil)
		etag := res.Header.Get("ETag")
		res.Body.Close()
		if etag == "" {
			t.Fatal("no ETag on a content-hashed asset — a hard reload re-pulls the whole bundle")
		}
		res = do(http.MethodGet, "/assets/"+asset, map[string]string{"If-None-Match": etag})
		res.Body.Close()
		if res.StatusCode != http.StatusNotModified {
			t.Errorf("If-None-Match = %d, want 304", res.StatusCode)
		}
	})

	t.Run("unknown and directory asset paths are 404 not HTML", func(t *testing.T) {
		for _, p := range []string{"/assets/nope.js", "/assets/", "/assets"} {
			res := do(http.MethodGet, p, nil)
			body := make([]byte, 512)
			n, _ := res.Body.Read(body)
			res.Body.Close()
			if res.StatusCode != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404 (body %q)", p, res.StatusCode, body[:n])
			}
			if ct := res.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
				t.Errorf("GET %s served %s — an asset URL must never return the SPA shell", p, ct)
			}
		}
	})
	t.Run("SPA shell is served at the root", func(t *testing.T) {
		res := do(http.MethodGet, "/", nil)
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET / = %d, want 200", res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q, want text/html", ct)
		}
	})

	t.Run("HEAD on the SPA root is served, not rejected", func(t *testing.T) {
		res := do(http.MethodHead, "/", nil)
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("HEAD / = %d, want 200 — load-balancer probes must work", res.StatusCode)
		}
	})

	t.Run("unmatched API paths never fall through to the SPA shell", func(t *testing.T) {
		// Browsers send Accept: */*, which defeats litespaserver's JSON guard, so
		// an API path that no route claims must still fail loudly rather than
		// return index.html with a 200. "/api" and bare forms matter because
		// PathPrefix("/api/") does not match them.
		for _, p := range []string{"/api", "/api/v1", "/api/v1/nope", "/api/nope"} {
			for _, accept := range []string{"*/*", "application/json"} {
				res := do(http.MethodGet, p, map[string]string{"Accept": accept})
				body := make([]byte, 512)
				n, _ := res.Body.Read(body)
				res.Body.Close()
				if res.StatusCode == http.StatusOK {
					t.Errorf("Accept %q: GET %s = 200 — an unmatched API path falls through to the SPA shell and fails silently as an empty tab (body %q)", accept, p, body[:n])
				}
				if ct := res.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
					t.Errorf("Accept %q: GET %s served %s, want a JSON or 404 response", accept, p, ct)
				}
			}
		}
	})

	t.Run("the not-found guard does not shadow a real API route", func(t *testing.T) {
		// The guard is registered after PathPrefix("/api/v1/"); if that order ever
		// flips, login and every other endpoint start returning 404 while every
		// build and vet gate still passes. Assert a live route survives.
		res := do(http.MethodGet, "/api/v1/me", map[string]string{"Accept": "application/json"})
		body := make([]byte, 256)
		n, _ := res.Body.Read(body)
		res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			t.Fatalf("GET /api/v1/me = 404 — the /api/ guard is shadowing the console API (body %q)", body[:n])
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("GET /api/v1/me served %s, want application/json", ct)
		}
	})
}

// TestCopyProxiedHeaderSingleValue pins the invariant that a proxied module
// response carries at most one value per copied header. handleInvoke appends
// with Add so multi-valued headers survive, and the router has already declared
// defaults on the same map — without the delete the reply ships two disagreeing
// values, and without the cache-control refusal a module could make user-scoped
// output publicly cacheable.
func TestCopyProxiedHeaderSingleValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		preset map[string]string
		key    string
		values []string
		want   []string
	}{
		{"default replaced not appended", map[string]string{"Cache-Control": "no-store", "ETag": `"router-default"`}, "ETag", []string{`"module-etag"`}, []string{`"module-etag"`}},
		{"multi-valued header kept in full", nil, "X-Request-Id", []string{"a", "b"}, []string{"a", "b"}},
		{"absent key is a no-op", map[string]string{"Other": "1"}, "Etag", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			for k, v := range tt.preset {
				h.Set(k, v)
			}
			consoleserver.CopyProxiedHeader(h, tt.key, tt.values)
			if got := h.Values(tt.key); len(got) != len(tt.want) {
				t.Fatalf("Values(%q) = %v, want %v", tt.key, got, tt.want)
			} else {
				for i := range got {
					if got[i] != tt.want[i] {
						t.Fatalf("Values(%q)[%d] = %q, want %q", tt.key, i, got[i], tt.want[i])
					}
				}
			}
			if tt.key == "ETag" && h.Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q, the router default must survive copying an ETag", h.Get("Cache-Control"))
			}
		})
	}

	t.Run("cache-control is never copied", func(t *testing.T) {
		h := http.Header{}
		h.Set("Cache-Control", "no-store")
		consoleserver.CopyProxiedHeader(h, "Cache-Control", []string{"public, max-age=86400"})
		consoleserver.CopyProxiedHeader(h, "cache-control", []string{"public"})
		if got := h.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store — module output is user-scoped and must not become cacheable", got)
		}
	})
}

// TestOptionsAsteriskDoesNotBecomeHTML covers OPTIONS *, whose request target is
// not a path. Left to mux it is treated as a path needing cleanup, answered with
// a 301 to /%2A, and the SPA catch-all then serves index.html for it — a 200 with
// HTML where nothing sensible was asked for. Written on a raw socket because the
// Go client will not send an "*" target.
func TestOptionsAsteriskDoesNotBecomeHTML(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := consoleserver.NewConsoleHandler(ctx, nil, nil, nil, nil)
	// Same server option production sets: without it net/http answers OPTIONS *
	// before the console handler ever runs, and the test would measure Go's
	// default handler rather than this package's behaviour.
	ts := httptest.NewUnstartedServer(h)
	consoleserver.ApplyServerDefaults(ts.Config)
	ts.Start()
	defer ts.Close()

	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := fmt.Fprint(conn, "OPTIONS * HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	res.Body.Close()

	if res.StatusCode == http.StatusMovedPermanently || res.StatusCode == http.StatusOK {
		t.Fatalf("OPTIONS * = %d; it must not redirect or serve the SPA", res.StatusCode)
	}
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("OPTIONS * = %d, want 405", res.StatusCode)
	}
	if res.Header.Get("Allow") == "" {
		t.Error("405 must name the allowed methods")
	}
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff — OPTIONS is inside the header contract", got)
	}
}

// TestConsoleResponseHeaders pins the security and caching headers across every
// response class the console router can produce, including the ones gorilla/mux
// answers itself. mux applies route middleware only to matched handlers, so 404,
// 405 and path-cleanup 301 responses bypass every handler in this package — the
// reason the headers are declared by a wrapper above the router rather than per
// route.
func TestConsoleResponseHeaders(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := consoleserver.NewConsoleHandler(ctx, nil, nil, nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	asset := firstAssetJS(t)

	cases := []struct {
		name        string
		method      string
		path        string
		wantStatus  int
		wantCacheIn string // substring Cache-Control must contain
		noImmutable bool
	}{
		{"hashed asset", http.MethodGet, "/assets/" + asset, http.StatusOK, "immutable", false},
		{"missing asset", http.MethodGet, "/assets/nope.js", http.StatusNotFound, "no-store", true},
		{"bare assets", http.MethodGet, "/assets", http.StatusNotFound, "no-store", true},
		{"unmatched api", http.MethodGet, "/api/nope", http.StatusNotFound, "no-store", true},
		{"wrong method on asset", http.MethodDelete, "/assets/" + asset, http.StatusMethodNotAllowed, "no-store", true},
		{"wrong method on api", http.MethodPost, "/api/nope", http.StatusMethodNotAllowed, "no-store", true},
		{"user-scoped json api", http.MethodGet, "/api/v1/me", http.StatusOK, "no-store", true},
		{"spa shell", http.MethodGet, "/", http.StatusOK, "no-store", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(ctx, tc.method, ts.URL+tc.path, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			res, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", tc.method, tc.path, err)
			}
			res.Body.Close()

			if res.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}
			for hdr, want := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"X-Frame-Options":        "SAMEORIGIN",
				"Referrer-Policy":        "origin-when-cross-origin",
			} {
				if got := res.Header.Get(hdr); got != want {
					t.Errorf("%s = %q, want %q — every response class must carry it, including mux's own", hdr, got, want)
				}
			}
			if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, tc.wantCacheIn) {
				t.Errorf("Cache-Control = %q, want it to contain %q", cc, tc.wantCacheIn)
			}
			if tc.noImmutable && strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
				t.Errorf("Cache-Control = %q must not be immutable", res.Header.Get("Cache-Control"))
			}
		})
	}
}

// TestShellStreamSurvivesHeaderWrapper is the discriminator for the console
// header wrapper. server.shellCreate asserts w.(http.Flusher) on the writer it
// is handed; any struct that wraps ResponseWriter fails that assertion and the
// cloud shell starts answering 500 "streaming unsupported" instead of reaching
// the agent lookup. A 404 therefore means the streaming path is still intact.
func TestShellStreamSurvivesHeaderWrapper(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := server.NewServer(15 * time.Second)
	h := consoleserver.NewConsoleHandler(ctx, nil, srv.Reg, nil, srv)
	ts := httptest.NewServer(h)
	defer ts.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/shell/connect?agent=absent&id=0123456789ab", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("shell connect: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode == http.StatusInternalServerError {
		t.Fatalf("shell connect = 500 (%s): the header wrapper hides http.Flusher from server.shellCreate", bytes.TrimSpace(body))
	}
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("shell connect = %d, want 404 (no such agent)", res.StatusCode)
	}
	// A route-order slip would send this to consoleapi's own 404 and still look
	// like a pass, so pin which handler answered.
	if body := string(bytes.TrimSpace(body)); !strings.Contains(body, "agent not connected") {
		t.Errorf("shell connect body = %q, want the shell handler's %q", body, "agent not connected")
	}
}

// TestMuxCleanupRedirectCarriesHeaders covers the third response class the
// router cannot produce itself: gorilla/mux answers a path that needs cleaning
// with a 301 before any route matches. Go's http client normalises the path and
// then follows the redirect, so this has to be written on a raw socket.
func TestMuxCleanupRedirectCarriesHeaders(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := consoleserver.NewConsoleHandler(ctx, nil, nil, nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := fmt.Fprintf(conn, "GET //assets/%s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", firstAssetJS(t), ts.Listener.Addr()); err != nil {
		t.Fatalf("write request: %v", err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	res.Body.Close()

	if res.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301 (the double slash must be cleaned)", res.StatusCode)
	}
	// A cached 301 pins a wrong prefix on the client, so this response in
	// particular must not be storable.
	for hdr, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "SAMEORIGIN",
		"Referrer-Policy":        "origin-when-cross-origin",
	} {
		if got := res.Header.Get(hdr); got != want {
			t.Errorf("%s = %q, want %q", hdr, got, want)
		}
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store on a redirect", cc)
	}
}
