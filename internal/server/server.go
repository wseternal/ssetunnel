package server

import (
	"io"
	"log"
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

// recreateHandler builds a new Handler with the given auth store and metrics
// collector, preserving state (callbacks, persistent shell sessions) from the
// previous handler. All handler recreation goes through this single method
// to avoid silently dropping state.
func (s *Server) recreateHandler(store *auth.Store, mc *metrics.MetricsCollector) {
	prev := s.handler
	s.handler = NewHandlerWithMetrics(s.Reg, prev.heartbeat, store, mc, s.basePath)
	s.handler.OnSession = s.attach
	s.handler.OnUpPush = prev.OnUpPush
	s.handler.shellSessions = prev.shellSessions // preserve persistent shell sessions
}

// SetAuthStore attaches an authentication store for token validation.
func (s *Server) SetAuthStore(store *auth.Store) {
	s.store = store
	s.recreateHandler(store, s.metrics)
}

// SetMetricsCollector attaches a metrics collector for transport monitoring
// and auto-tuning. Must be called before serving; recreates the handler
// to pick up the collector.
func (s *Server) SetMetricsCollector(mc *metrics.MetricsCollector) {
	s.metrics = mc
	s.recreateHandler(s.store, mc)
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

// attach wraps a newly registered tunnel session in a yamux server,
// then accepts the control stream that the agent opens. The agent
// opens the control stream (not the server) so the server's accept
// loop never needs to distinguish control from data streams.
func (s *Server) attach(sess *Session) {
	ms, err := mux.Server(sess)
	if err != nil {
		sess.Close()
		return
	}
	sess.SetYamuxSession(ms)

	// Accept the control stream from the agent. The agent opens it
	// immediately after the yamux session is established, so Accept
	// returns quickly. If the connection drops, Accept returns an
	// error and the session dies naturally.
	go func() {
		cs, err := ms.Accept()
		if err != nil {
			log.Printf("server: accept control stream failed for session %s: %v", sess.ID(), err)
			return
		}
		// Read and verify the control marker.
		marker := make([]byte, len(mux.ControlStreamMarker))
		if _, err := io.ReadFull(cs, marker); err != nil || string(marker) != mux.ControlStreamMarker {
			log.Printf("server: bad control stream marker for session %s: %v", sess.ID(), err)
			cs.Close()
			return
		}
		sess.SetControlStream(cs)
		log.Printf("server: control stream accepted for session %s", sess.ID())
	}()
}

// HTTPHandler returns the tunnel endpoint handler (/events, /up).
func (s *Server) HTTPHandler() http.Handler { return s.handler }

// TunnelHandler returns the underlying tunnel handler for external mounting.
// Used by the console server to proxy shell connect requests through the
// existing /connect and /connect-up endpoints.
func (s *Server) TunnelHandler() *Handler { return s.handler }

// StartShellCleanup starts the background goroutine that periodically
// cleans up idle persistent shell sessions. It stops when done is closed.
func (s *Server) StartShellCleanup(done <-chan struct{}) {
	s.handler.shellSessions.StartCleanupLoop(30*time.Second, shellIdleTimeout, done)
}

// CloseShellSessions closes all persistent shell sessions.
func (s *Server) CloseShellSessions() {
	s.handler.shellSessions.CloseAll()
}

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


