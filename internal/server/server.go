package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/mux"
)

var bufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 32*1024)
		return &buf
	},
}

// Server is the public side of the tunnel: the HTTP endpoints plus the
// TCP entry listener users connect to.
type Server struct {
	// Reg is the live session registry (exported for tests and the console).
	Reg *Registry

	handler *Handler
	store   *auth.Store
}

// NewServer builds a server given an SSE heartbeat interval.
func NewServer(heartbeat time.Duration) *Server {
	reg := NewRegistry()
	h := NewHandler(reg, heartbeat)
	s := &Server{Reg: reg, handler: h}
	h.OnSession = s.attach
	return s
}

// NewServerWithRegistry builds a server with a given registry and SSE heartbeat interval.
func NewServerWithRegistry(reg *Registry, heartbeat time.Duration) *Server {
	h := NewHandler(reg, heartbeat)
	s := &Server{Reg: reg, handler: h}
	h.OnSession = s.attach
	return s
}

// SetAuthStore attaches an authentication store for token validation.
func (s *Server) SetAuthStore(store *auth.Store) {
	s.store = store
	s.handler = NewHandlerWithAuth(s.Reg, s.handler.heartbeat, store)
	s.handler.OnSession = s.attach
}

// AttachSession manually attaches a session (useful for custom flows and testing).
func (s *Server) AttachSession(sess *Session) {
	s.attach(sess)
}

// AttachConn manually attaches any net.Conn (useful for direct multiplexing tests).
func (s *Server) AttachConn(conn net.Conn) {
	ms, err := mux.Server(conn)
	if err != nil {
		conn.Close()
		return
	}
	// Create a synthetic session wrapping the conn for test compatibility.
	sess := NewSession("test-" + conn.LocalAddr().String())
	sess.SetYamuxSession(ms)
	s.Reg.Replace(sess)
}

// attach wraps a newly registered tunnel session in a yamux server and
// stores it on the session. Each session has its own independent yamux.
func (s *Server) attach(sess *Session) {
	ms, err := mux.Server(sess)
	if err != nil {
		sess.Close()
		return
	}
	sess.SetYamuxSession(ms)
}

// HTTPHandler returns the tunnel endpoint handler (/events, /up).
func (s *Server) HTTPHandler() http.Handler { return s.handler }

// NewHTTPServer builds the production HTTP server.
func (s *Server) NewHTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // must not kill SSE
		// IdleTimeout bounds how long the server keeps a TCP connection
		// alive between requests. Go's zero default means 60 s, which
		// silently closes idle POST connections; the agent's transport
		// then reuses a stale connection, gets EOF on the next POST,
		// and the yamux session dies. 5 minutes gives the yamux
		// keepalive (30 s) a 10× margin.
		IdleTimeout: 5 * time.Minute,
	}
}

// ServeEntry accepts user TCP conns until ctx is done.
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

// proxyEntry opens a yamux stream on the current session and copies bidirectionally.
func (s *Server) proxyEntry(c net.Conn) {
	if s.store != nil {
		if !s.authenticateEntryConn(c) {
			return
		}
	}

	ms := s.findYamux()
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

	go func() {
		buf := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(buf)
		_, _ = io.CopyBuffer(stream, c, *buf)
		stream.Close()
	}()
	go func() {
		buf := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(buf)
		_, _ = io.CopyBuffer(c, stream, *buf)
		c.Close()
	}()
}

// authenticateEntryConn reads the first line from c as a token and validates
// it. On success it writes "OK\n" and clears the read deadline. On failure it
// writes "ERR unauthorized\n" before closing c, so clients are not left
// guessing why the connection was dropped.
func (s *Server) authenticateEntryConn(c net.Conn) bool {
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(c)
	tokenLine, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("server: entry handshake failed from %s: %v", c.RemoteAddr(), err)
		fmt.Fprintf(c, "ERR unauthorized\n") //nolint:errcheck // best-effort
		c.Close()
		return false
	}

	tokenStr := strings.TrimSpace(tokenLine)

	// Validate user session
	sessInfo, err := s.store.ValidateUserSession(context.Background(), tokenStr)
	if err == nil && auth.HasPermission(sessInfo.Role, auth.PermConnect) {
		if _, err := fmt.Fprintf(c, "OK\n"); err != nil {
			c.Close()
			return false
		}
		c.SetReadDeadline(time.Time{})
		return true
	}

	log.Printf("server: entry handshake rejected invalid token from %s", c.RemoteAddr())
	fmt.Fprintf(c, "ERR unauthorized\n") //nolint:errcheck // best-effort
	c.Close()
	return false
}

// findYamux returns the first open yamux session from the registry.
func (s *Server) findYamux() *yamux.Session {
	var ms *yamux.Session
	s.Reg.Range(func(sess *Session) bool {
		if m := sess.YamuxSession(); m != nil && !m.IsClosed() {
			ms = m
			return false // stop at first
		}
		return true
	})
	return ms
}
