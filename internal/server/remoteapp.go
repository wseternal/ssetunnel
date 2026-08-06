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
	"sync"
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
	// Fail closed: if sess is nil (race during reconnect), deny non-admin users.
	sessInfo := UserSessionFromContext(r)
	if !isAdmin(sessInfo) {
		if sess == nil || sess.UserID() != sessInfo.UserID {
			http.Error(w, "agent not found or access denied", http.StatusNotFound)
			return
		}
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
		resize:  nil, // remote app does not use PTY resize; nil-channel is safe in select
		cancel:  func() {},
	}
	bridgeDone := make(chan struct{})
	h.connectSessions.Store(id, cs)
	defer func() {
		cs.up.Close()
		stream.Close()
		<-bridgeDone // wait for bridge goroutine to exit
		h.connectSessions.Delete(id)
		h.metrics.RecordSessionEnd(agentID)
	}()

	// streamMu serializes writes to the yamux stream from the bridge
	// goroutine (input events) and the SSE loop (ACK frames).
	var streamMu sync.Mutex

	// Bridge goroutine: read framed input events from the pipe → write to yamux.
	go func() {
		defer close(bridgeDone)
		buf := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(buf)
		for {
			n, err := cs.up.Read(*buf)
			if n > 0 {
				streamMu.Lock()
				_, werr := stream.Write((*buf)[:n])
				streamMu.Unlock()
				if werr != nil {
					log.Printf("remoteapp: bridge write error agent=%s session=%s: %v", agentID, id, werr)
					stream.Close()
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("remoteapp: bridge read error agent=%s session=%s: %v", agentID, id, err)
				}
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

	h.metrics.RecordSessionStart(agentID)
	log.Printf("remoteapp: session started agent=%s session=%s user=%d", agentID, id, ownerID)

	// Inject server event: stream opened.
	if werr := writeSSELogEvent(w, f, "info", "server", "stream opened to agent"); werr != nil {
		return
	}

	// Emit "stream closed" on every exit path (normal disconnect, error, etc.).
	defer func() {
		_ = writeSSELogEvent(w, f, "info", "server", "stream closed")
	}()

	// Reusable buffer for reading frames from the agent (avoids per-frame alloc).
	readBuf := make([]byte, remoteapp.MaxFrameSize())

	// SSE loop: read typed frames from yamux stream → write SSE events.
	// The agent sends:
	//   - FrameScreenshot (0x01): [8-byte BE timestamp][JPEG] → base64 SSE data frame + ACK
	//   - FrameScreenInfo (0x03): JSON screen info → SSE "screeninfo" event
	//   - FrameLogEvent (0x04): JSON log event → SSE "log" event (observability)
	for {
		stream.SetReadDeadline(time.Now().Add(h.heartbeat))
		frameType, n, err := remoteapp.ReadFrameInto(stream, readBuf)
		if err != nil {
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				// Heartbeat.
				if sess != nil {
					select {
					case <-sess.Done():
						_ = writeSSELogEvent(w, f, "info", "server", "agent session died")
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
			_ = writeSSELogEvent(w, f, "error", "server", fmt.Sprintf("SSE stream read error: %v", err))
			return // stream closed
		}

		switch frameType {
		case remoteapp.FrameScreenshot:
			// Parse and strip the 8-byte timestamp prefix from the payload.
			// Forward only the JPEG data to the frontend; ACK back to agent.
			ts, jpegData, ok := remoteapp.ParseScreenshotTimestamp(readBuf[:n])
			if !ok {
				// Malformed: payload shorter than timestamp prefix.
				// Skip this frame rather than sending corrupt data.
				log.Printf("remoteapp: malformed screenshot frame (no timestamp) agent=%s session=%s", agentID, id)
				continue
			}
			if werr := writeSSEDataFrame(w, f, jpegData); werr != nil {
				return
			}
			h.metrics.RecordConnectBytes(agentID, 0, len(jpegData))
			// Send ACK back to the agent via yamux stream.
			streamMu.Lock()
			ackErr := remoteapp.WriteScreenshotAck(stream, ts)
			streamMu.Unlock()
			if ackErr != nil {
				log.Printf("remoteapp: write ACK agent=%s session=%s: %v", agentID, id, ackErr)
				return
			}
		case remoteapp.FrameScreenInfo:
			// Write as named SSE event so frontend can distinguish.
			if werr := writeSSENamedFrame(w, f, "screeninfo", readBuf[:n]); werr != nil {
				return
			}
			h.metrics.RecordConnectBytes(agentID, 0, n)
		case remoteapp.FrameLogEvent:
			// Validate and sanitize agent log event before forwarding.
			var logEvt remoteapp.LogEvent
			if err := json.Unmarshal(readBuf[:n], &logEvt); err != nil {
				continue // skip malformed log events
			}
			logEvt.Source = "agent" // enforce provenance
			switch logEvt.Severity {
			case "info", "warn", "error":
			default:
				logEvt.Severity = "info"
			}
			if len(logEvt.Message) > 1024 {
				logEvt.Message = logEvt.Message[:1024]
			}
			sanitized, err := json.Marshal(logEvt)
			if err != nil {
				continue
			}
			if werr := writeSSENamedFrame(w, f, "log", sanitized); werr != nil {
				return
			}
			h.metrics.RecordConnectBytes(agentID, 0, n)
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
	// When auth is disabled, UserSessionMiddleware injects a synthetic admin with
	// UserID=0, so sessInfo is never nil here.
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
	if !remoteapp.ValidateInputEventType(evt.Type) {
		http.Error(w, "unknown input event type", http.StatusBadRequest)
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

// writeSSEBase64 writes a base64-encoded SSE frame using a streaming encoder
// to avoid allocating a full base64 string copy. If eventName is empty, writes
// a default "data:" frame; otherwise writes a named "event:" frame.
func writeSSEBase64(w io.Writer, f http.Flusher, eventName string, payload []byte) error {
	if eventName != "" {
		if _, err := fmt.Fprintf(w, "event: %s\ndata: ", eventName); err != nil {
			return err
		}
	} else {
		if _, err := io.WriteString(w, "data: "); err != nil {
			return err
		}
	}
	enc := base64.NewEncoder(base64.StdEncoding, w)
	if _, err := enc.Write(payload); err != nil {
		enc.Close()
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "\n\n"); err != nil {
		return err
	}
	f.Flush()
	return nil
}

// writeSSEDataFrame writes a base64-encoded SSE data frame.
func writeSSEDataFrame(w io.Writer, f http.Flusher, payload []byte) error {
	return writeSSEBase64(w, f, "", payload)
}

// writeSSENamedFrame writes a base64-encoded SSE named event frame.
func writeSSENamedFrame(w io.Writer, f http.Flusher, eventName string, payload []byte) error {
	return writeSSEBase64(w, f, eventName, payload)
}

// writeSSELogEvent writes a server-originated log event as an SSE "log" event.
// The payload is a JSON LogEvent with source="server".
func writeSSELogEvent(w io.Writer, f http.Flusher, severity, source, message string) error {
	evt := remoteapp.LogEvent{
		TS:       time.Now().UTC().Format(time.RFC3339Nano),
		Severity: severity,
		Source:   source,
		Message:  message,
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	return writeSSENamedFrame(w, f, "log", data)
}
