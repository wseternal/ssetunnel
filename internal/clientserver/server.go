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
	"sync"
	"time"

	"github.com/gorilla/mux"
	clientassets "github.com/wseternal/ssetunnel/frontend/client"
)

// Config controls the browser server startup.
type Config struct {
	// Listen is the loopback address to bind (default "127.0.0.1:0").
	// Non-loopback addresses are rejected: the launch secret is printed to
	// the terminal and visible in process arguments, so the API must never
	// be reachable from the network.
	Listen string
	// Version is the build version string, logged at startup.
	Version string
}

// Server is the secured local HTTP server for the browser client.
type Server struct {
	listener  net.Listener
	secret    string
	baseURL   string
	handler   http.Handler
	indexHTML []byte
	agent     *agentRuntime
	connects  *connectRegistry
	serverCtx context.Context // lives as long as the server
	version   string
}

// New creates a new browser server bound to the configured loopback address.
func New(cfg Config) (*Server, error) {
	listen := cfg.Listen
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	if err := validateLoopbackListen(listen); err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", listen, err)
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		ln.Close()
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	secret := hex.EncodeToString(b)

	s := &Server{
		listener:  ln,
		secret:    secret,
		baseURL:   fmt.Sprintf("http://%s/#%s", ln.Addr().String(), secret),
		agent:     &agentRuntime{},
		connects:  newConnectRegistry(context.Background()),
		serverCtx: context.Background(),
		version:   cfg.Version,
	}
	// The handler is built once; agent/connect runtimes started through it
	// are tied to the context passed to Serve, not to this background ctx.
	s.handler = s.buildHandler()
	return s, nil
}

// validateLoopbackListen rejects any bind address that is not loopback.
// The Host header check is not a compensating control: a non-browser client
// controls its own Host header, so a wildcard bind would expose the API to
// the network with only the launch secret as a barrier.
func validateLoopbackListen(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", listen, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("ui: --listen must be a loopback address (127.0.0.1, localhost, ::1), got %q", listen)
	}
	return nil
}

// Addr returns the bound address.
func (s *Server) Addr() string {
	return s.listener.Addr().String()
}

// URL returns the authenticated loopback URL with the launch secret in the fragment.
func (s *Server) URL() string {
	return s.baseURL
}

// Serve starts the HTTP server and blocks until the context is cancelled
// and all owned runtimes are torn down.
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
		// net/http answers OPTIONS * itself, before the handler, so that
		// response would bypass the security headers the wrapper declares.
		DisableGeneralOptionsHandler: true,
	}

	teardownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		// Drain HTTP first so no in-flight request can register a new
		// runtime after teardown has cleared the registries.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("ui: http shutdown: %v", err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := s.agent.stop(); err != nil {
				log.Printf("ui: agent teardown: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			s.connects.stopAll()
		}()
		wg.Wait()
		close(teardownDone)
	}()

	if s.version != "" {
		log.Printf("ui: ssetunnel %s", s.version)
	}
	log.Printf("ui: listening on %s", s.listener.Addr().String())
	if err := srv.Serve(s.listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	// On context shutdown, Serve's return must mean everything is closed.
	if ctx.Err() != nil {
		select {
		case <-teardownDone:
		case <-time.After(teardownTimeout + 5*time.Second):
			log.Printf("ui: runtime teardown did not complete; exiting")
		}
	}
	return nil
}

// buildHandler creates the mux router with security middleware and routes.
// The client SPA is a single page with no router and no streaming, so it is
// served by dedicated index/asset handlers instead of litespaserver: that
// library Set()s its own headers over any outer wrapper and puts no-store
// on hashed assets. The securityHeaders wrapper above mux covers every
// response class, including mux-generated 404/405/301.
func (s *Server) buildHandler() http.Handler {
	index, err := fs.ReadFile(clientassets.ClientWebRootFs, "index.html")
	if err != nil {
		log.Printf("ui: ERROR embedded client/dist has no index.html (%v); the SPA will not load", err)
	} else {
		s.indexHTML = index
	}

	r := mux.NewRouter()

	// API routes (require launch secret). requestLogging is registered
	// first (outermost) so rejected requests — bad secrets, rebinding
	// attempts — are logged too, not just authenticated ones.
	api := r.PathPrefix("/api").Subrouter()
	api.Use(requestLogging)
	api.Use(s.authMiddleware)
	s.registerAPIRoutes(api)

	// Explicit 404 for unmatched API paths, including exact /api (the
	// subrouter matcher does not catch it; the SPA catch-all would).
	api.PathPrefix("/").HandlerFunc(notFound)
	r.Path("/api").HandlerFunc(notFound)

	// Hashed assets: immutable, served directly from the embedded FS.
	r.PathPrefix("/assets/").HandlerFunc(s.serveAsset).Methods("GET", "HEAD")
	r.Path("/assets").HandlerFunc(notFound).Methods("GET", "HEAD")

	// SPA catch-all — must stay last.
	r.PathPrefix("/").HandlerFunc(s.serveIndex).Methods("GET", "HEAD")

	return securityHeaders(r)
}

// allowedOriginHosts is the set of hostnames accepted for the Origin header.
var allowedOriginHosts = map[string]bool{
	"127.0.0.1": true,
	"localhost": true,
	"::1":       true,
}

// authMiddleware enforces the launch secret on every API request.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Exact Host check — reject DNS rebinding. SplitHostPort handles
		// the bracketed IPv6 form ("[::1]:port"); a bare host passes
		// through with brackets stripped ("[::1]" without a port).
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		} else {
			host = strings.Trim(host, "[]")
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}

		// Same-origin Origin check when present: scheme, loopback hostname,
		// and port must all match the request's own origin.
		if origin := r.Header.Get("Origin"); origin != "" {
			u, parseErr := url.Parse(origin)
			if parseErr != nil || u.Scheme != "http" || !allowedOriginHosts[u.Hostname()] {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
			if originPort(u) != requestPort(r.Host) {
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

func originPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	return "80"
}

func requestPort(host string) string {
	if _, p, err := net.SplitHostPort(host); err == nil {
		return p
	}
	return "80"
}

// securityHeaders applies the ADR-005 browser security contract to every
// response, including mux-generated errors. Handlers may override individual
// values (the asset handler sets immutable caching); nothing downstream
// re-sets them wholesale, which is why litespaserver is not used here.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; form-action 'self'; base-uri 'self'")
		h.Set("Cache-Control", "no-store")
		if r.URL.Path == "*" {
			h.Set("Allow", "OPTIONS, GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// notFound writes a 404 response; the wrapper supplies the security headers.
func notFound(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}
