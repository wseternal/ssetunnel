package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/wseternal/ssetunnel/internal/remoteapp"
	"github.com/wseternal/ssetunnel/internal/transport"
)

// TargetRemoteApp is the magic target name for remote desktop sessions.
// Must match the constant in the agent package.
const TargetRemoteApp = "__remote_app__"

// RemoteAppConnectHandler returns an http.Handler that serves the remote
// desktop connect endpoint (SSE downstream). It wraps the existing connect
// infrastructure with forced target=__remote_app__ and user-scoped access.
func (h *Handler) RemoteAppConnectHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Validate user session auth.
		sessInfo := UserSessionFromContext(r)
		if sessInfo == nil {
			http.Error(w, "Unauthorized: user session required", http.StatusUnauthorized)
			return
		}

		agentID := r.URL.Query().Get("agent")
		if agentID == "" {
			http.Error(w, "agent query parameter is required", http.StatusBadRequest)
			return
		}

		// User-scoped access: non-admin users can only access their own agents.
		if !isAdmin(sessInfo) {
			if !h.agentOwnedByUser(agentID, sessInfo.UserID) {
				http.Error(w, "agent not found or access denied", http.StatusNotFound)
				return
			}
		}

		// Force target to __remote_app__ via context key.
		q := r.URL.Query()
		q.Set("target", "")
		r.URL.RawQuery = q.Encode()

		ctx := context.WithValue(r.Context(), forcedTargetKey, TargetRemoteApp)
		h.handleRemoteApp(w, r.WithContext(ctx))
	})
}

// RemoteAppConnectUpHandler returns an http.Handler that serves the remote
// desktop upstream POST endpoint. It wraps input JSON as a typed frame
// before writing to the connect session's pipe.
func (h *Handler) RemoteAppConnectUpHandler() http.Handler {
	return http.HandlerFunc(h.handleRemoteAppUp)
}

// handleRemoteApp serves GET /remoteapp/connect: the remote desktop SSE
// downstream. It opens a yamux stream to the agent, runs a frame-aware
// bridge (typed length-prefixed frames on the yamux side, SSE on the HTTP side).
func (h *Handler) handleRemoteApp(w http.ResponseWriter, r *http.Request) {
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

	// Find the target agent's yamux session.
	// Short-poll: wait up to 3 seconds for the agent to appear.
	const connectWaitTimeout = 3 * time.Second
	const connectPollInterval = 25 * time.Millisecond
	var ms *yamux.Session
	var sess *Session
	deadline := time.Now().Add(connectWaitTimeout)
	for {
		ms, sess = h.findYamuxByAgentID(agentID)
		if ms != nil && !ms.IsClosed() {
			break
		}
		if time.Now().After(deadline) || r.Context().Err() != nil {
			http.Error(w, fmt.Sprintf("agent %q not connected", agentID), http.StatusNotFound)
			return
		}
		time.Sleep(connectPollInterval)
	}

	// Narrow TOCTOU: re-check session is alive before opening stream.
	if ms.IsClosed() {
		http.Error(w, "agent session replaced, retry", http.StatusServiceUnavailable)
		return
	}

	// Re-verify ownership against the resolved session (TOCTOU guard).
	sessInfo := UserSessionFromContext(r)
	if !isAdmin(sessInfo) && sess != nil && sess.UserID() != sessInfo.UserID {
		http.Error(w, "agent not found or access denied", http.StatusNotFound)
		return
	}

	// Open a yamux stream to the agent.
	stream, err := ms.OpenStream()
	if err != nil {
		http.Error(w, fmt.Sprintf("open stream: %v", err), http.StatusServiceUnavailable)
		return
	}

	// Write target header for the agent (dynamic target mode).
	if sess != nil && sess.WantTarget() {
		if _, err := fmt.Fprintf(stream, "%s\n", TargetRemoteApp); err != nil {
			stream.Close()
			http.Error(w, fmt.Sprintf("write target header: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Create the connect bridge: up pipe for input events → yamux stream.
	var ownerID int64
	if sessInfo != nil {
		ownerID = sessInfo.UserID
	}
	cs := &connectSession{
		id:      id,
		agentID: agentID,
		userID:  ownerID,
		up:      transport.NewPipe(connectUpPipeCap),
		resize:  make(chan windowSize, 1), // unused for remote app but required by struct
		cancel:  func() {},
	}
	h.connectSessions.Store(id, cs)
	defer func() {
		cs.up.Close()
		stream.Close()
		h.connectSessions.Delete(id)
	}()

	// Bridge goroutine: read framed input events from the pipe → write to yamux.
	// The connect-up handler already wraps POST bodies as typed frames, so
	// this is a raw byte copy (same pattern as shell).
	go func() {
		buf := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(buf)
		for {
			n, err := cs.up.Read(*buf)
			if n > 0 {
				if _, werr := stream.Write((*buf)[:n]); werr != nil {
					stream.Close()
					return
				}
			}
			if err != nil {
				stream.Close()
				return
			}
		}
	}()

	// Detect agent session death.
	if sess != nil {
		go func() {
			select {
			case <-sess.Done():
				stream.Close()
			case <-r.Context().Done():
			}
		}()
	}

	// Set up SSE response headers.
	w.Header().Set("X-SSET-Session", id)
	transport.WriteHeaders(w)
	f.Flush()

	// SSE loop: read typed frames from yamux stream → write SSE events.
	// The agent sends:
	//   - FrameScreenshot (0x01): JPEG data → base64 SSE data frame
	//   - FrameScreenInfo (0x03): JSON screen info → SSE "screeninfo" event
	for {
		stream.SetReadDeadline(time.Now().Add(h.heartbeat))
		frameType, data, err := remoteapp.ReadFrame(stream)
		if err != nil {
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				// Heartbeat.
				if sess != nil {
					select {
					case <-sess.Done():
						return
					default:
					}
				}
				if werr := transport.WriteHeartbeat(w, f); werr != nil {
					return
				}
				continue
			}
			log.Printf("remoteapp: SSE loop read error agent=%s session=%s: %v", agentID, id, err)
			return // stream closed
		}

		switch frameType {
		case remoteapp.FrameScreenshot:
			// Write as default SSE data frame (base64-encoded JPEG).
			if werr := writeSSEDataFrame(w, f, data); werr != nil {
				return
			}
		case remoteapp.FrameScreenInfo:
			// Write as named SSE event so frontend can distinguish.
			if werr := writeSSENamedFrame(w, f, "screeninfo", data); werr != nil {
				return
			}
		default:
			// Unknown frame type from agent; skip.
		}
	}
}

// handleRemoteAppUp serves POST /remoteapp/connect-up: wraps the JSON input
// event body as a typed frame and writes it to the connect session's pipe.
func (h *Handler) handleRemoteAppUp(w http.ResponseWriter, r *http.Request) {
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

	// Verify session ownership: non-admin users can only write to their own sessions.
	// When auth is disabled (store == nil), sessInfo is nil and this check is skipped.
	sessInfo := UserSessionFromContext(r)
	if sessInfo == nil && cs.userID != 0 {
		// Auth is enabled but session info missing — reject.
		http.Error(w, "Unauthorized: user session required", http.StatusUnauthorized)
		return
	}
	if sessInfo != nil && !isAdmin(sessInfo) && cs.userID != 0 && sessInfo.UserID != cs.userID {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}

	// Reject X-SSET-Flags (no reorder window on remoteapp connect-up).
	if flags := r.Header.Get("X-SSET-Flags"); flags != "" {
		http.Error(w, "X-SSET-Flags not supported on remoteapp connect-up", http.StatusBadRequest)
		return
	}

	// Read and validate the JSON input event body.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var evt remoteapp.InputEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Wrap as typed frame using WriteFrame for atomic construction.
	var frameBuf bytes.Buffer
	if err := remoteapp.WriteFrame(&frameBuf, remoteapp.FrameInput, body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	frame := frameBuf.Bytes()

	if r.Context().Err() != nil {
		http.Error(w, "client disconnected", http.StatusGone)
		return
	}
	cs.up.SetWriteDeadline(time.Now().Add(defaultWriteTimeout))
	if _, err := cs.up.Write(frame); err != nil {
		http.Error(w, "session closed", http.StatusConflict)
		return
	}
	h.metrics.RecordConnectBytes(cs.agentID, len(frame), 0)
	w.WriteHeader(http.StatusOK)
}

// writeSSEDataFrame writes a base64-encoded SSE data frame.
func writeSSEDataFrame(w io.Writer, f http.Flusher, payload []byte) error {
	encoded := base64.StdEncoding.EncodeToString(payload)
	if _, err := fmt.Fprintf(w, "data: %s\n\n", encoded); err != nil {
		return err
	}
	f.Flush()
	return nil
}

// writeSSENamedFrame writes a base64-encoded SSE named event frame.
func writeSSENamedFrame(w io.Writer, f http.Flusher, eventName string, payload []byte) error {
	encoded := base64.StdEncoding.EncodeToString(payload)
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, encoded); err != nil {
		return err
	}
	f.Flush()
	return nil
}
