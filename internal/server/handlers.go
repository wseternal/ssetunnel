package server

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

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
type Handler struct {
	reg       *Registry
	heartbeat time.Duration
	mux       *http.ServeMux

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
	h := &Handler{reg: reg, heartbeat: heartbeat, mux: http.NewServeMux()}
	agentAuth := AgentAuthMiddleware(store)
	h.mux.Handle("/events", agentAuth(http.HandlerFunc(h.handleEvents)))
	h.mux.Handle("/up", agentAuth(http.HandlerFunc(h.handleUp)))
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
	// Store agent identity and capabilities for entry routing.
	if agentID := r.Header.Get("X-SSET-Agent-ID"); agentID != "" {
		sess.SetAgentID(agentID)
	}
	if r.Header.Get("X-SSET-Target") == "true" {
		sess.SetWantTarget(true)
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
