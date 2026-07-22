package server

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/wseternal/ssetunnel/internal/transport"
)

// maxUpBody defensively caps one POST body. The protocol target is one
// 16 KiB batch (plan decision 4), but a single large Write can make the
// batcher overshoot, so the guard sits well above that.
const maxUpBody = 1 << 20

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
}

// NewHandler builds the tunnel handler. heartbeat is the SSE keepalive
// interval — a struct field, so tests use tiny values (plan decision 10).
func NewHandler(reg *Registry, heartbeat time.Duration) *Handler {
	h := &Handler{reg: reg, heartbeat: heartbeat, mux: http.NewServeMux()}
	h.mux.HandleFunc("/events", h.handleEvents)
	h.mux.HandleFunc("/up", h.handleUp)
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
	h.reg.Replace(sess)
	defer h.reg.Remove(id, sess) // deregister on death; no-op if replaced
	if h.OnSession != nil {
		h.OnSession(sess)
	}

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
	w.WriteHeader(sess.push(seq, body))
}
