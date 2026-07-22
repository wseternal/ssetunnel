package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/wseternal/ssetunnel/internal/mux"
)

// Server is the public side of the tunnel: the HTTP endpoints plus the
// TCP entry listener users connect to (plan step 7).
type Server struct {
	// Reg is the live session registry (exported for tests and the
	// future console).
	Reg *Registry

	handler *Handler

	mu   sync.Mutex
	sess *yamux.Session // yamux server over the current tunnel session
}

// NewServer wires the registry, handlers, and session→yamux attachment.
// heartbeat is the SSE keepalive interval (15 s in production, tiny in
// tests per plan decision 10).
func NewServer(heartbeat time.Duration) *Server {
	reg := NewRegistry()
	h := NewHandler(reg, heartbeat)
	s := &Server{Reg: reg, handler: h}
	h.OnSession = s.attach
	return s
}

// attach wraps a newly registered tunnel session in a yamux server and
// makes it current. Called by the events handler on every connect.
func (s *Server) attach(sess *Session) {
	ms, err := mux.Server(sess)
	if err != nil {
		sess.Close()
		return
	}
	s.mu.Lock()
	old := s.sess
	s.sess = ms
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

// HTTPHandler returns the tunnel endpoint handler (/events, /up).
func (s *Server) HTTPHandler() http.Handler { return s.handler }

// NewHTTPServer builds the production HTTP server. WriteTimeout MUST
// stay 0 — any write timeout kills the long-lived SSE stream (plan
// decision 7). ReadHeaderTimeout bounds slowloris; ReadTimeout bounds
// slow-dribble POST bodies (≤1 MiB, well under 30 s) — the SSE GET has
// no request body, so it is unaffected.
func (s *Server) NewHTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // must not kill SSE
	}
}

// ServeEntry accepts user TCP conns until ctx is done, proxying each
// over its own yamux stream (plan step 7: one stream per accepted conn).
func (s *Server) ServeEntry(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept entry conn: %w", err)
		}
		go s.proxyEntry(c)
	}
}

// proxyEntry opens a yamux stream on the current session and copies
// bidirectionally. With no active session the conn is closed cleanly —
// users retry rather than hang (spec: no hung connections).
func (s *Server) proxyEntry(c net.Conn) {
	s.mu.Lock()
	ms := s.sess
	s.mu.Unlock()
	if ms == nil || ms.IsClosed() {
		log.Printf("server: entry conn from %s closed: no active session", c.RemoteAddr())
		c.Close()
		return
	}
	stream, err := ms.OpenStream()
	if err != nil {
		log.Printf("server: open stream for %s: %v", c.RemoteAddr(), err)
		c.Close()
		return
	}
	go func() { io.Copy(stream, c); stream.Close() }()
	go func() { io.Copy(c, stream); c.Close() }()
}
