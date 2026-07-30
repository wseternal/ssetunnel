package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/metrics"
	"github.com/wseternal/ssetunnel/internal/mux"
)

var bufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 32*1024)
		return &buf
	},
}

// Server is the public side of the tunnel: the HTTP endpoints that serve
// agent transport (/events, /up) and connect client transport (/connect, /connect-up).
type Server struct {
	// Reg is the live session registry (exported for tests and the console).
	Reg *Registry

	handler   *Handler
	store     *auth.Store
	metrics   *metrics.MetricsCollector
	basePath  string // HTTP path prefix for all endpoints (empty = no prefix)
}

// NewServer builds a server given an SSE heartbeat interval.
func NewServer(heartbeat time.Duration) *Server {
	return NewServerWithBase(heartbeat, "")
}

// NewServerWithBase builds a server with an HTTP path prefix for all
// tunnel endpoints (empty = no prefix).
func NewServerWithBase(heartbeat time.Duration, basePath string) *Server {
	return NewServerWithRegistryAndBase(NewRegistry(), heartbeat, basePath)
}

// NewServerWithRegistry builds a server with a given registry and SSE heartbeat interval.
func NewServerWithRegistry(reg *Registry, heartbeat time.Duration) *Server {
	return NewServerWithRegistryAndBase(reg, heartbeat, "")
}

// NewServerWithRegistryAndBase builds a server with a given registry,
// SSE heartbeat interval, and HTTP path prefix.
func NewServerWithRegistryAndBase(reg *Registry, heartbeat time.Duration, basePath string) *Server {
	basePath = strings.TrimRight(basePath, "/")
	h := NewHandlerWithAuth(reg, heartbeat, nil, basePath)
	s := &Server{Reg: reg, handler: h, basePath: basePath}
	h.OnSession = s.attach
	return s
}

// SetAuthStore attaches an authentication store for token validation.
func (s *Server) SetAuthStore(store *auth.Store) {
	s.store = store
	prev := s.handler
	s.handler = NewHandlerWithMetrics(s.Reg, s.handler.heartbeat, store, s.metrics, s.basePath)
	s.handler.OnSession = s.attach
	s.handler.OnUpPush = prev.OnUpPush
}

// SetMetricsCollector attaches a metrics collector for transport monitoring
// and auto-tuning. Must be called before serving; recreates the handler
// to pick up the collector.
func (s *Server) SetMetricsCollector(mc *metrics.MetricsCollector) {
	s.metrics = mc
	prev := s.handler
	s.handler = NewHandlerWithMetrics(s.Reg, s.handler.heartbeat, s.store, mc, s.basePath)
	s.handler.OnSession = s.attach
	s.handler.OnUpPush = prev.OnUpPush
}

// MetricsCollector returns the attached metrics collector (may be nil).
func (s *Server) MetricsCollector() *metrics.MetricsCollector {
	return s.metrics
}

// FindSession returns the session for a given agent ID, or nil if not found.
// Used by the auto-tuner to push tune frames to agents.
func (s *Server) FindSession(agentID string) *Session {
	var found *Session
	s.Reg.Range(func(sess *Session) bool {
		if sess.AgentID() == agentID {
			found = sess
			return false
		}
		return true
	})
	return found
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


