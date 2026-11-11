// Package clientserver implements the secured local HTTP server for the
// browser client. It binds to a loopback address, generates a random launch
// secret, and enforces the browser security contract (exact Host, same-origin
// Origin, constant-time secret comparison, security headers, body limits).
package clientserver

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/wseternal/ssetunnel/frontend"
)

// Config controls the browser server startup.
type Config struct {
	// Listen is the loopback address to bind (default "127.0.0.1:0").
	Listen string
	// Secret overrides the generated launch secret (for testing).
	Secret string
}

// Server is the secured local HTTP server for the browser client.
type Server struct {
	listener net.Listener
	secret   string
	baseURL  string
	handler  http.Handler
}

// New creates a new browser server bound to the configured loopback address.
func New(cfg Config) (*Server, error) {
	listen := cfg.Listen
	if listen == "" {
		listen = "127.0.0.1:0"
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", listen, err)
	}

	secret := cfg.Secret
	if secret == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			ln.Close()
			return nil, fmt.Errorf("generate secret: %w", err)
		}
		secret = hex.EncodeToString(b)
	}

	s := &Server{
		listener: ln,
		secret:   secret,
		baseURL:  fmt.Sprintf("http://%s/#%s", ln.Addr().String(), secret),
	}
	s.handler = s.buildHandler()
	return s, nil
}

// Addr returns the bound address.
func (s *Server) Addr() string {
	return s.listener.Addr().String()
}

// URL returns the authenticated loopback URL with the launch secret in the fragment.
func (s *Server) URL() string {
	return s.baseURL
}

// Serve starts the HTTP server and blocks until the context is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("ui: listening on %s", s.listener.Addr().String())
	if err := srv.Serve(s.listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// buildHandler creates the mux router with security middleware and routes.
func (s *Server) buildHandler() http.Handler {
	r := mux.NewRouter()

	// API routes (require launch secret).
	api := r.PathPrefix("/api").Subrouter()
	api.Use(s.authMiddleware)
	api.Use(securityHeaders)
	s.registerAPIRoutes(api)

	// Explicit 404 for unmatched API paths.
	api.PathPrefix("/").HandlerFunc(notFound)

	// Static assets from embedded client bundle.
	clientFS := frontend.ClientWebRootFs
	r.PathPrefix("/assets/").Handler(spaHeaders(http.StripPrefix("/assets/", http.FileServer(http.FS(clientFS)))))

	// SPA fallback — serves index.html for unmatched paths.
	r.PathPrefix("/").Handler(spaHeaders(spaFallbackHandler(clientFS)))

	return r
}

// spaFallbackHandler serves the SPA index.html for any unmatched GET/HEAD request.
func spaFallbackHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		// Try to serve the requested file directly.
		if f, err := fsys.Open(strings.TrimPrefix(r.URL.Path, "/")); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// Fallback to index.html for SPA routing.
		data, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
}

// allowedOriginHosts is the set of hostnames accepted for the Origin header.
var allowedOriginHosts = map[string]bool{
	"127.0.0.1": true,
	"localhost":  true,
	"::1":        true,
}

// authMiddleware enforces the launch secret on every API request.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Exact Host check — reject DNS rebinding.
		host := r.Host
		if idx := strings.IndexByte(host, ':'); idx > 0 {
			host = host[:idx]
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}

		// Same-origin Origin check when present — parse and compare hostname exactly.
		if origin := r.Header.Get("Origin"); origin != "" {
			u, parseErr := url.Parse(origin)
			if parseErr != nil || !allowedOriginHosts[u.Hostname()] {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}

		// Constant-time secret comparison.
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.secret)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// securityHeaders applies CSP, nosniff, frame, referrer, and cache headers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; form-action 'self'; base-uri 'self'")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// spaHeaders wraps a handler with no-cache and security headers for the SPA.
func spaHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// notFound writes a 404 response with security headers.
func notFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	http.NotFound(w, nil)
}

// indexExists checks whether the embedded client bundle has an index.html.
func indexExists() bool {
	_, err := fs.Stat(frontend.ClientWebRootFs, "index.html")
	return err == nil
}
