package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/transport"
)

// maxUpBody defensively caps one POST body at the 1 MiB batch ceiling
// plus 64 KiB of slack: the batch ceiling must never equal the body cap
// (cycle-2 plan decision 8), or an exactly-at-ceiling batch would 413 —
// and 413 means session death.
const maxUpBody = 1<<20 + 64<<10

// maxProbeBody caps one /probe body (cycle-2 plan decision 6: read and
// discard, bounded surface).
const maxProbeBody = 2 << 20

// Server capabilities advertised on the /events 200 response (cycle-2
// plan decision 3: consts, not config).
const (
	capsConcurrency = 4
	capsBatch       = 1 << 20
	capsHeaderValue = "concurrency=4;batch=1048576;gzip"
)

// Handler serves the tunnel endpoints: GET /events?id= streams the
// downstream SSE for a session, POST /up carries upstream batches with
// X-SSET-Session / X-SSET-Seq headers (plan decision 4).
// GET /connect and POST /connect-up serve the connect client's HTTP transport.
type Handler struct {
	reg       *Registry
	heartbeat time.Duration
	store     *auth.Store
	mux       *http.ServeMux

	// connectSessions maps connect session IDs to connectSession structs,
	// bridging the connect client's HTTP transport to the agent's yamux stream.
	connectSessions sync.Map

	// OnSession, if set, is called after each session registers. Set it
	// before serving; the server uses it to attach yamux (step 7).
	OnSession func(*Session)

	// OnUpPush, if set, is called by handleUp just before pushing a
	// validated body into the session; a non-nil channel it returns is
	// waited on first. Test-only hook (precedent: OnSession) that lets
	// tests deterministically shuffle concurrent POST delivery — never
	// set it in production.
	OnUpPush func(seq uint64) <-chan struct{}
}

// NewHandler builds the tunnel handler without auth.
func NewHandler(reg *Registry, heartbeat time.Duration) *Handler {
	return NewHandlerWithAuth(reg, heartbeat, nil)
}

// NewHandlerWithAuth builds the tunnel handler with an optional auth store.
func NewHandlerWithAuth(reg *Registry, heartbeat time.Duration, store *auth.Store) *Handler {
	h := &Handler{reg: reg, heartbeat: heartbeat, store: store, mux: http.NewServeMux()}
	agentAuth := AgentAuthMiddleware(store)
	connectAuth := ConnectAuthMiddleware(store)
	h.mux.Handle("/events", agentAuth(http.HandlerFunc(h.handleEvents)))
	h.mux.Handle("/up", agentAuth(http.HandlerFunc(h.handleUp)))
	h.mux.Handle("/connect", connectAuth(http.HandlerFunc(h.handleConnect)))
	h.mux.Handle("/connect-up", connectAuth(http.HandlerFunc(h.handleConnectUp)))
	h.mux.HandleFunc("/probe", h.handleProbe)
	return h
}

// ServeHTTP routes /events and /up.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// handleEvents registers a fresh session for the ID and streams its
// downstream bytes as SSE frames until the session or client goes away.
// A reconnect with the same ID replaces the stale session (decision 5).
func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	sess := NewSession(id)
	// Two-way negotiation, fail closed (cycle-2 plan decision 3): the
	// agent echoes its chosen caps on the request; only concurrency>1
	// arms the reorder window, everything else keeps the legacy path.
	if parseCapsConcurrency(r.Header.Get("X-SSET-Caps")) > 1 {
		sess.enableWindow()
	}
	// Store agent identity and capabilities for agent routing.
	if agentID := r.Header.Get("X-SSET-Agent-ID"); agentID != "" {
		sess.SetAgentID(agentID)
	}
	if r.Header.Get("X-SSET-Target") == "true" {
		sess.SetWantTarget(true)
	}
	// Capture user ownership from auth context for console session filtering.
	if sessInfo := UserSessionFromContext(r); sessInfo != nil {
		sess.SetUserID(sessInfo.UserID)
	} else if tokInfo := TokenInfoFromContext(r); tokInfo != nil && tokInfo.UserID != nil {
		sess.SetUserID(*tokInfo.UserID)
	}
	h.reg.Replace(sess)
	defer h.reg.Remove(id, sess) // deregister on death; no-op if replaced
	if h.OnSession != nil {
		h.OnSession(sess)
	}

	w.Header().Set("X-SSET-Caps", capsHeaderValue)
	transport.WriteHeaders(w)
	f.Flush()

	// Client disconnect = session death (fail-fast, decision 3); the
	// agent reconnects with a fresh ID.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-r.Context().Done():
			sess.Close()
		case <-stop:
		}
	}()

	buf := make([]byte, 32<<10)
	for {
		// The read deadline doubles as the heartbeat timer: on timeout
		// send a comment keepalive instead of data.
		sess.down.SetReadDeadline(time.Now().Add(h.heartbeat))
		n, err := sess.down.Read(buf)
		if n > 0 {
			if werr := transport.WriteFrame(w, f, buf[:n]); werr != nil {
				return
			}
		}
		if err == nil {
			continue
		}
		var nerr net.Error
		if errors.As(err, &nerr) && nerr.Timeout() {
			if werr := transport.WriteHeartbeat(w, f); werr != nil {
				return
			}
			continue
		}
		return // session closed
	}
}

// handleUp accepts one upstream batch. Unknown session or seq gap fails
// fast with 409 (decisions 1+3); an old seq is a deduped retry → 200.
func (h *Handler) handleUp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.Header.Get("X-SSET-Session")
	seqHeader := r.Header.Get("X-SSET-Seq")
	if id == "" || seqHeader == "" {
		http.Error(w, "missing X-SSET-Session or X-SSET-Seq", http.StatusBadRequest)
		return
	}
	seq, err := strconv.ParseUint(seqHeader, 10, 64)
	if err != nil {
		http.Error(w, "bad X-SSET-Seq", http.StatusBadRequest)
		return
	}
	sess := h.reg.Get(id)
	if sess == nil {
		http.Error(w, "unknown session", http.StatusConflict)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxUpBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "read body", http.StatusBadRequest)
		}
		return
	}
	// X-SSET-Flags (cycle-2 plan decision 5): fail closed — unknown flag
	// values and gzip on a non-negotiated session are 400, never errors
	// silently ignored.
	if flags := r.Header.Get("X-SSET-Flags"); flags != "" {
		for flag := range strings.SplitSeq(flags, ",") {
			switch strings.TrimSpace(flag) {
			case "gzip":
				if !sess.hasWindow() {
					http.Error(w, "gzip flag on non-negotiated session", http.StatusBadRequest)
					return
				}
				zr, err := gzip.NewReader(bytes.NewReader(body))
				if err != nil {
					http.Error(w, "bad gzip body", http.StatusBadRequest)
					return
				}
				raw, err := io.ReadAll(io.LimitReader(zr, maxUpBody))
				zr.Close()
				if err != nil {
					http.Error(w, "bad gzip body", http.StatusBadRequest)
					return
				}
				body = raw
			default:
				http.Error(w, "unknown X-SSET-Flags value", http.StatusBadRequest)
				return
			}
		}
	}
	if h.OnUpPush != nil {
		if gate := h.OnUpPush(seq); gate != nil {
			<-gate
		}
	}
	w.WriteHeader(sess.push(seq, body))
}

// handleProbe reads and discards its body and returns 200 (cycle-2 plan
// decision 6). It registers no session: probing must not hijack the live
// agent's yamux session the way /events would.
func (h *Handler) handleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxProbeBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "read body", http.StatusBadRequest)
		}
		return
	}
	w.WriteHeader(http.StatusOK)
}

// parseCapsConcurrency extracts the concurrency value from an X-SSET-Caps
// header ("concurrency=4;batch=1048576;gzip"). Absent or malformed input
// yields 0 — negotiation fails closed to cycle-1 behavior, never errors.
func parseCapsConcurrency(h string) int {
	for field := range strings.SplitSeq(h, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok || k != "concurrency" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 {
			return 0
		}
		return n
	}
	return 0
}

// ---------------------------------------------------------------------------
// Connect endpoint: HTTP transport for connect clients (replaces TCP listener)
// ---------------------------------------------------------------------------

// connectSession bridges a connect client's HTTP transport (SSE-down +
// POST-up) to the agent's yamux stream. It is NOT registered in the
// session registry (which is for agent tunnel sessions only).
type connectSession struct {
	id     string
	up     *transport.Pipe // POST bodies → yamux stream
	cancel context.CancelFunc
}

// handleConnect serves GET /connect: the connect client's SSE downstream.
// It authenticates via ConnectAuthMiddleware, finds the target agent's yamux
// session, opens a stream, and bridges bidirectionally between the HTTP
// transport and the yamux stream.
func (h *Handler) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	q := r.URL.Query()
	id := q.Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	agentID := q.Get("agent")
	target := q.Get("target")

	// Find the target agent's yamux session.
	// Short-poll: if no agent is available yet (e.g., agent reconnecting
	// after a session kill), wait up to connectWaitTimeout for one to
	// appear. This avoids forcing the connect client into expensive
	// backoff retries when the agent is momentarily between sessions.
	const connectWaitTimeout = 3 * time.Second
	const connectPollInterval = 25 * time.Millisecond
	var ms *yamux.Session
	var sess *Session
	deadline := time.Now().Add(connectWaitTimeout)
	for {
		if agentID != "" {
			ms, sess = h.findYamuxByAgentID(agentID)
		} else {
			ms, sess = h.findYamux()
		}
		if ms != nil && !ms.IsClosed() {
			break
		}
		if time.Now().After(deadline) || r.Context().Err() != nil {
			if agentID != "" {
				http.Error(w, fmt.Sprintf("agent %q not connected", agentID), http.StatusNotFound)
			} else {
				http.Error(w, "no active agent session", http.StatusNotFound)
			}
			return
		}
		time.Sleep(connectPollInterval)
	}

	// Validate target if the connect client specified one and auth is enabled.
	if target != "" && h.store != nil {
		lookupID := agentID
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		cfg, err := h.store.GetAgentConfig(ctx, lookupID)
		if err != nil {
			http.Error(w, fmt.Sprintf("agent config not found for %q", lookupID), http.StatusNotFound)
			return
		}
		if !auth.TargetAllowed(cfg.AllowedTargets, target) {
			http.Error(w, fmt.Sprintf("target %q not allowed", target), http.StatusForbidden)
			return
		}
	}

	// Re-check the session is still alive before opening a stream — the
	// agent may have reconnected between the short-poll loop and here,
	// causing Registry.Replace to close the old yamux session (TOCTOU).
	if ms.IsClosed() {
		http.Error(w, "agent session replaced, retry", http.StatusServiceUnavailable)
		return
	}

	// Open a yamux stream to the agent.
	stream, err := ms.OpenStream()
	if err != nil {
		http.Error(w, fmt.Sprintf("open stream: %v", err), http.StatusServiceUnavailable)
		return
	}

	// Write target header if the agent wants it (dynamic target mode).
	if target != "" && sess != nil && sess.WantTarget() {
		if _, err := fmt.Fprintf(stream, "%s\n", target); err != nil {
			stream.Close()
			http.Error(w, fmt.Sprintf("write target header: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Create the connect bridge: up pipe for POST bodies → yamux stream.
	_, ctxCancel := context.WithCancel(r.Context())
	cs := &connectSession{
		id:     id,
		up:     transport.NewPipe(downPipeCap),
		cancel: ctxCancel,
	}
	h.connectSessions.Store(id, cs)
	defer func() {
		ctxCancel()
		cs.up.Close()
		stream.Close()
		h.connectSessions.Delete(id)
	}()

	// Goroutine: copy upstream data from pipe → yamux stream.
	go func() {
		buf := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(buf)
		io.CopyBuffer(stream, cs.up, *buf)
		stream.Close() // signal EOF to agent
	}()

	// Set up SSE response headers.
	w.Header().Set("X-SSET-Session", id)
	transport.WriteHeaders(w)
	f.Flush()

	// SSE loop: read from yamux stream → write SSE frames to client.
	// Uses read deadline as heartbeat timer (same pattern as handleEvents).
	// On heartbeat, also checks session health via sess.Done() to detect
	// session death promptly (Session.Close() does NOT close yamux streams).
	sseBuf := make([]byte, 32<<10)
	for {
		stream.SetReadDeadline(time.Now().Add(h.heartbeat))
		n, err := stream.Read(sseBuf)
		if n > 0 {
			if werr := transport.WriteFrame(w, f, sseBuf[:n]); werr != nil {
				return
			}
		}
		if err == nil {
			continue
		}
		var nerr net.Error
		if errors.As(err, &nerr) && nerr.Timeout() {
			// Check if the agent session died while we waited.
			if sess != nil {
				select {
				case <-sess.Done():
					return // session killed
				default:
				}
			}
			if werr := transport.WriteHeartbeat(w, f); werr != nil {
				return
			}
			continue
		}
		return // stream closed
	}
}

// handleConnectUp serves POST /connect-up: the connect client's batched upstream.
// It looks up the connect session by X-SSET-Session header and writes the POST
// body into the session's up pipe (which feeds the yamux stream).
func (h *Handler) handleConnectUp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.Header.Get("X-SSET-Session")
	if id == "" {
		http.Error(w, "missing X-SSET-Session", http.StatusBadRequest)
		return
	}

	v, ok := h.connectSessions.Load(id)
	if !ok {
		http.Error(w, "unknown connect session", http.StatusConflict)
		return
	}
	cs := v.(*connectSession)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxUpBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "read body", http.StatusBadRequest)
		}
		return
	}

	// Write to the up pipe with a deadline to avoid blocking indefinitely.
	// Check request context first: if the client disconnected mid-POST,
	// don't block on a write the client will never see.
	if r.Context().Err() != nil {
		http.Error(w, "client disconnected", http.StatusGone)
		return
	}
	cs.up.SetWriteDeadline(time.Now().Add(defaultWriteTimeout))
	if _, err := cs.up.Write(body); err != nil {
		http.Error(w, "session closed", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// findYamux returns the first open yamux session and its Session from the registry.
func (h *Handler) findYamux() (*yamux.Session, *Session) {
	var ms *yamux.Session
	var found *Session
	h.reg.Range(func(sess *Session) bool {
		if m := sess.YamuxSession(); m != nil && !m.IsClosed() {
			ms = m
			found = sess
			return false // stop at first
		}
		return true
	})
	return ms, found
}

// findYamuxByAgentID returns the yamux session and Session for an agent with
// the given agentID. Returns (nil, nil) if no matching agent is found.
func (h *Handler) findYamuxByAgentID(agentID string) (*yamux.Session, *Session) {
	var ms *yamux.Session
	var found *Session
	h.reg.Range(func(sess *Session) bool {
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
