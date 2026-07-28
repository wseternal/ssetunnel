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

// entryRequest holds the parsed fields from the entry handshake line.
type entryRequest struct {
	token   string // bearer token (always present)
	agentID string // agent routing key (empty = first-match)
	target  string // dynamic target address (empty = no dynamic target)
}

// proxyEntry opens a yamux stream on the agent's session and copies bidirectionally.
// It handles agent ID routing, target validation, and target header writing.
func (s *Server) proxyEntry(c net.Conn) {
	var req *entryRequest
	if s.store != nil {
		// Auth mode: read TOKEN [agent_id [target]]\n handshake.
		req = s.parseEntryHandshake(c)
		if req == nil {
			return // handshake failed, connection already closed
		}
		if !s.validateEntryAuth(c, req.token) {
			return
		}
	} else {
		// No-auth mode: backward compat — no handshake, data flows directly.
		req = &entryRequest{}
	}

	// Find the target agent session (before sending OK so errors are reported
	// as handshake failures, not mid-stream disconnections).
	var ms *yamux.Session
	var sess *Session
	if req.agentID != "" {
		ms, sess = s.findYamuxByAgentID(req.agentID)
		if ms == nil || ms.IsClosed() {
			log.Printf("server: entry conn from %s: agent %q not found", c.RemoteAddr(), req.agentID)
			fmt.Fprintf(c, "ERR agent %q not connected\n", req.agentID) //nolint:errcheck
			c.Close()
			return
		}
	} else {
		ms = s.findYamux()
		if ms == nil || ms.IsClosed() {
			log.Printf("server: entry conn from %s: no active session", c.RemoteAddr())
			fmt.Fprintf(c, "ERR no active agent session\n") //nolint:errcheck
			c.Close()
			return
		}
	}

	// Validate target if the connect client specified one.
	if req.target != "" && s.store != nil {
		// Look up agent config (falls back to NULL default row).
		agentID := req.agentID
		cfg, err := s.store.GetAgentConfig(context.Background(), agentID)
		if err != nil {
			log.Printf("server: agent config lookup for %q: %v", agentID, err)
			fmt.Fprintf(c, "ERR agent config not found for %q\n", agentID) //nolint:errcheck
			c.Close()
			return
		}
		if !auth.TargetAllowed(cfg.AllowedTargets, req.target) {
			log.Printf("server: target %s not allowed for agent %q", req.target, agentID)
			fmt.Fprintf(c, "ERR target %q not allowed\n", req.target) //nolint:errcheck
			c.Close()
			return
		}
	}

	// All validations passed — send OK so the client can proceed.
	if s.store != nil {
		if _, err := fmt.Fprintf(c, "OK\n"); err != nil {
			c.Close()
			return
		}
	}

	stream, err := ms.OpenStream()
	if err != nil {
		log.Printf("server: open stream for %s: %v", c.RemoteAddr(), err)
		c.Close()
		return
	}

	// Write target header if the agent wants it (dynamic target mode).
	if req.target != "" && sess != nil && sess.WantTarget() {
		if _, err := fmt.Fprintf(stream, "%s\n", req.target); err != nil {
			log.Printf("server: write target header: %v", err)
			stream.Close()
			c.Close()
			return
		}
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

// parseEntryHandshake reads the first line from c and parses it as
// TOKEN [agent_id [target]]\n. Returns nil on failure (connection closed).
func (s *Server) parseEntryHandshake(c net.Conn) *entryRequest {
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(c)
	line, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("server: entry handshake failed from %s: %v", c.RemoteAddr(), err)
		fmt.Fprintf(c, "ERR unauthorized\n") //nolint:errcheck
		c.Close()
		return nil
	}
	c.SetReadDeadline(time.Time{})

	line = strings.TrimSpace(line)
	parts := strings.SplitN(line, " ", 3)

	req := &entryRequest{token: parts[0]}
	if len(parts) >= 2 {
		req.agentID = parts[1]
	}
	if len(parts) >= 3 {
		req.target = parts[2]
	}

	// NOTE: bufio.Reader may have consumed bytes beyond the \n.
	// For backward compat (old clients that only send TOKEN\n), this
	// is fine because no data flows before OK\n. For new clients,
	// the connect client must not pipeline data before receiving OK\n.
	// If buffered bytes exist, they belong to the post-handshake data
	// stream and we need to handle them. For now, the protocol ensures
	// no pipelining — OK\n must be received before data flows.
	return req
}

// validateEntryAuth checks the token against the auth store.
func (s *Server) validateEntryAuth(c net.Conn, token string) bool {
	sessInfo, err := s.store.ValidateUserSession(context.Background(), token)
	if err == nil && auth.UserHasPermission(sessInfo.Role, sessInfo.PermConnect, sessInfo.PermAgent, auth.PermConnect) {
		return true
	}
	log.Printf("server: entry handshake rejected invalid token from %s", c.RemoteAddr())
	fmt.Fprintf(c, "ERR unauthorized\n") //nolint:errcheck
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

// findYamuxByAgentID returns the yamux session and Session for an agent with
// the given agentID. Returns (nil, nil) if no matching agent is found.
func (s *Server) findYamuxByAgentID(agentID string) (*yamux.Session, *Session) {
	var ms *yamux.Session
	var found *Session
	s.Reg.Range(func(sess *Session) bool {
		if sess.AgentID() == agentID {
			if m := sess.YamuxSession(); m != nil && !m.IsClosed() {
				ms = m
				found = sess
				return false
			}
		}
		return true
	})
	return ms, found
}
