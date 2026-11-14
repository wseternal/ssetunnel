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
	"github.com/visdomtech/orcacommon/litespaserver"
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
	listener  net.Listener
	secret    string
	baseURL   string
	handler   http.Handler
	agent     *agentRuntime
	connects  *connectRegistry
	serverCtx context.Context // lives as long as the server
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
		listener:  ln,
		secret:    secret,
		baseURL:   fmt.Sprintf("http://%s/#%s", ln.Addr().String(), secret),
		agent:     &agentRuntime{},
		serverCtx: context.Background(),
	}
	// Build handler with a cancellable context so the agent/connect
	// runtimes are torn down when Serve exits.
	s.handler = s.buildHandler(context.Background())
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
	// Store the server context so API handlers can start agents/connects
	// whose lifetime is tied to the server.
	s.serverCtx = ctx
	// Initialize the connect registry with the server context so all
	// named connect sessions are cancelled when the server shuts down.
	s.connects = newConnectRegistry(ctx)

	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		// Stop all agent/connect runtimes before HTTP shutdown.
		s.agent.stop()
		s.connects.stopAll()
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
func (s *Server) buildHandler(ctx context.Context) http.Handler {
	r := mux.NewRouter()

	// API routes (require launch secret).
	api := r.PathPrefix("/api").Subrouter()
	api.Use(s.authMiddleware)
	api.Use(securityHeaders)
	s.registerAPIRoutes(api)

	// Explicit 404 for unmatched API paths.
	api.PathPrefix("/").HandlerFunc(notFound)

	// SPA catch-all — uses litespaserver with embedded client bundle,
	// matching the console server pattern. StaticPaths lets it serve
	// hashed assets from /assets/** directly from the embedded FS.
	spaCfg := litespaserver.Config{
		EmbeddedContent: frontend.ClientWebRootFs,
		StaticPaths:     []string{"/assets/**"},
		CDNVersion:      "embedded",
		CSP: litespaserver.CSPConfig{
			Disable: true,
		},
	}
	spaServer := litespaserver.NewServer(ctx, nil, spaCfg)
	r.PathPrefix("/").HandlerFunc(spaServer.ServeRoot).Methods("GET", "HEAD")

	if _, err := fs.Stat(frontend.ClientWebRootFs, "index.html"); err != nil {
		log.Printf("ui: ERROR embedded client/dist has no index.html (%v); the SPA will not load", err)
	}

	return r
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

// notFound writes a 404 response with security headers.
func notFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	http.NotFound(w, nil)
}

