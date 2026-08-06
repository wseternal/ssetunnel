package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/remoteapp"
	"github.com/wseternal/ssetunnel/internal/transport"
)

// injectUserSession stores a UserSessionInfo in the request context for testing.
func injectUserSession(r *http.Request, si *auth.UserSessionInfo) *http.Request {
	ctx := context.WithValue(r.Context(), userSessionKey, si)
	return r.WithContext(ctx)
}

// newTestConnectSession creates a connectSession and stores it in the handler's
// connectSessions map. Returns the session for pipe-read verification.
func newTestConnectSession(t *testing.T, h *Handler, id, agentID string, userID int64) *connectSession {
	t.Helper()
	cs := &connectSession{
		id:      id,
		agentID: agentID,
		userID:  userID,
		up:      transport.NewPipe(1 << 16),
		cancel:  func() {},
	}
	h.connectSessions.Store(id, cs)
	t.Cleanup(func() { h.connectSessions.Delete(id) })
	return cs
}

// adminSession returns a synthetic admin UserSessionInfo (auth-disabled mode).
func adminSession() *auth.UserSessionInfo {
	return &auth.UserSessionInfo{Role: "admin", PermConnect: true, PermAgent: true}
}

// userSession returns a non-admin user session with the given user ID.
func userSession(uid int64) *auth.UserSessionInfo {
	return &auth.UserSessionInfo{UserID: uid, Role: "user", PermConnect: true}
}

func TestHandleRemoteAppUp_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewRegistry(), time.Hour)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/remoteapp/connect-up", nil)
	req = injectUserSession(req, adminSession())
	h.handleRemoteAppUp(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("got %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleRemoteAppUp_MissingSessionHeader(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewRegistry(), time.Hour)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/remoteapp/connect-up", nil)
	req = injectUserSession(req, adminSession())
	h.handleRemoteAppUp(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleRemoteAppUp_UnknownSession(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewRegistry(), time.Hour)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/remoteapp/connect-up", nil)
	req.Header.Set("X-SSET-Session", "nonexistent")
	req = injectUserSession(req, adminSession())
	h.handleRemoteAppUp(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestHandleRemoteAppUp_OwnershipForbidden(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewRegistry(), time.Hour)
	// Session owned by user 1.
	newTestConnectSession(t, h, "sess1", "agent1", 1)

	rec := httptest.NewRecorder()
	body := []byte(`{"type":"mouse_move","x":10,"y":20}`)
	req := httptest.NewRequest("POST", "/remoteapp/connect-up", bytes.NewReader(body))
	req.Header.Set("X-SSET-Session", "sess1")
	// Non-admin user 2 tries to write.
	req = injectUserSession(req, userSession(2))
	h.handleRemoteAppUp(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("got %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestHandleRemoteAppUp_OwnershipAllowed(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewRegistry(), time.Hour)
	cs := newTestConnectSession(t, h, "sess1", "agent1", 1)

	rec := httptest.NewRecorder()
	body := []byte(`{"type":"mouse_move","x":10,"y":20}`)
	req := httptest.NewRequest("POST", "/remoteapp/connect-up", bytes.NewReader(body))
	req.Header.Set("X-SSET-Session", "sess1")
	// Same user (user 1) — should succeed.
	req = injectUserSession(req, userSession(1))
	h.handleRemoteAppUp(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want %d", rec.Code, http.StatusOK)
	}
	// Verify the pipe received a valid frame.
	cs.up.SetReadDeadline(time.Now().Add(time.Second))
	frameType, data, err := remoteapp.ReadFrame(cs.up)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if frameType != remoteapp.FrameInput {
		t.Errorf("frame type: got 0x%02x, want 0x%02x", frameType, remoteapp.FrameInput)
	}
	if !bytes.Equal(data, body) {
		t.Errorf("data: got %q, want %q", data, body)
	}
}

func TestHandleRemoteAppUp_AdminAccess(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewRegistry(), time.Hour)
	cs := newTestConnectSession(t, h, "sess1", "agent1", 1)

	rec := httptest.NewRecorder()
	body := []byte(`{"type":"key_tap","key":"a"}`)
	req := httptest.NewRequest("POST", "/remoteapp/connect-up", bytes.NewReader(body))
	req.Header.Set("X-SSET-Session", "sess1")
	// Admin can write to any session.
	req = injectUserSession(req, adminSession())
	h.handleRemoteAppUp(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want %d", rec.Code, http.StatusOK)
	}
	cs.up.SetReadDeadline(time.Now().Add(time.Second))
	frameType, _, err := remoteapp.ReadFrame(cs.up)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if frameType != remoteapp.FrameInput {
		t.Errorf("frame type: got 0x%02x, want 0x%02x", frameType, remoteapp.FrameInput)
	}
}

func TestHandleRemoteAppUp_XSSETFlagsRejected(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewRegistry(), time.Hour)
	newTestConnectSession(t, h, "sess1", "agent1", 0)

	rec := httptest.NewRecorder()
	body := []byte(`{"type":"mouse_move","x":10,"y":20}`)
	req := httptest.NewRequest("POST", "/remoteapp/connect-up", bytes.NewReader(body))
	req.Header.Set("X-SSET-Session", "sess1")
	req.Header.Set("X-SSET-Flags", "reorder") // should be rejected
	req = injectUserSession(req, adminSession())
	h.handleRemoteAppUp(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleRemoteAppUp_InvalidJSON(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewRegistry(), time.Hour)
	newTestConnectSession(t, h, "sess1", "agent1", 0)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/remoteapp/connect-up", bytes.NewReader([]byte("{invalid")))
	req.Header.Set("X-SSET-Session", "sess1")
	req = injectUserSession(req, adminSession())
	h.handleRemoteAppUp(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleRemoteAppUp_ValidWrite(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewRegistry(), time.Hour)
	cs := newTestConnectSession(t, h, "sess1", "agent1", 0)

	body := []byte(`{"type":"mouse_click","x":100,"y":200,"button":"left"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/remoteapp/connect-up", bytes.NewReader(body))
	req.Header.Set("X-SSET-Session", "sess1")
	req = injectUserSession(req, adminSession())
	h.handleRemoteAppUp(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want %d", rec.Code, http.StatusOK)
	}

	// Read the frame from the pipe and verify it's a valid WriteFrame-encoded
	// frame with FrameInput type and matching JSON body.
	cs.up.SetReadDeadline(time.Now().Add(time.Second))
	// Read 5-byte header.
	var hdr [5]byte
	if _, err := io.ReadFull(cs.up, hdr[:]); err != nil {
		t.Fatalf("ReadFull header: %v", err)
	}
	if hdr[0] != remoteapp.FrameInput {
		t.Errorf("frame type: got 0x%02x, want 0x%02x", hdr[0], remoteapp.FrameInput)
	}
	length := binary.BigEndian.Uint32(hdr[1:])
	if int(length) != len(body) {
		t.Errorf("frame length: got %d, want %d", length, len(body))
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(cs.up, data); err != nil {
		t.Fatalf("ReadFull data: %v", err)
	}
	if !bytes.Equal(data, body) {
		t.Errorf("frame data: got %q, want %q", data, body)
	}
}

func TestHandleRemoteAppUp_InvalidInputType(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewRegistry(), time.Hour)
	newTestConnectSession(t, h, "sess1", "agent1", 0)

	rec := httptest.NewRecorder()
	body := []byte(`{"type":"invalid_type","x":10,"y":20}`)
	req := httptest.NewRequest("POST", "/remoteapp/connect-up", bytes.NewReader(body))
	req.Header.Set("X-SSET-Session", "sess1")
	req = injectUserSession(req, adminSession())
	h.handleRemoteAppUp(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestWriteSSELogEvent(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	var f http.Flusher = rec

	if err := writeSSELogEvent(rec, f, "warn", "server", "test message"); err != nil {
		t.Fatalf("writeSSELogEvent: %v", err)
	}

	output := rec.Body.String()
	// Verify SSE format: "event: log\ndata: <base64>\n\n"
	if !strings.HasPrefix(output, "event: log\ndata: ") {
		t.Fatalf("unexpected SSE format: %q", output)
	}
	if !strings.HasSuffix(output, "\n\n") {
		t.Fatalf("SSE output should end with double newline: %q", output)
	}

	// Extract and decode base64 payload.
	dataLine := strings.TrimPrefix(output, "event: log\ndata: ")
	dataLine = strings.TrimSuffix(dataLine, "\n\n")
	decoded, err := base64.StdEncoding.DecodeString(dataLine)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}

	// Unmarshal and validate JSON.
	var evt remoteapp.LogEvent
	if err := json.Unmarshal(decoded, &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Severity != "warn" {
		t.Errorf("severity: got %q, want %q", evt.Severity, "warn")
	}
	if evt.Source != "server" {
		t.Errorf("source: got %q, want %q", evt.Source, "server")
	}
	if evt.Message != "test message" {
		t.Errorf("message: got %q, want %q", evt.Message, "test message")
	}
	if evt.TS == "" {
		t.Error("timestamp should not be empty")
	}
}
