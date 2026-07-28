package server

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newTestServer starts an httptest server with the real handlers.
func newTestServer(t *testing.T, heartbeat time.Duration) (*httptest.Server, *Registry) {
	t.Helper()
	reg := NewRegistry()
	srv := httptest.NewServer(NewHandler(reg, heartbeat))
	t.Cleanup(srv.Close)
	return srv, reg
}

// postUp issues one upstream POST and returns the status code.
func postUp(t *testing.T, baseURL, sessionID string, seq uint64, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/up", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if sessionID != "" {
		req.Header.Set("X-SSET-Session", sessionID)
	}
	req.Header.Set("X-SSET-Seq", strconv.FormatUint(seq, 10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /up: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// waitFor polls cond with a loose 2s deadline (count-based, not timing).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestServerPostToReadOrdered(t *testing.T) {
	t.Parallel()
	srv, reg := newTestServer(t, time.Hour)
	reg.Replace(NewSession("s"))
	for i, body := range []string{"a", "b", "c"} {
		if code := postUp(t, srv.URL, "s", uint64(i), []byte(body)); code != http.StatusOK {
			t.Fatalf("POST seq %d: got %d, want 200", i, code)
		}
	}
	sess := reg.Get("s")
	if sess == nil {
		t.Fatal("session not registered")
	}
	sess.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 3)
	if _, err := io.ReadFull(sess, buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf) != "abc" {
		t.Fatalf("read %q, want %q (POST order)", buf, "abc")
	}
}

func TestServerWriteToSSEFrames(t *testing.T) {
	t.Parallel()
	srv, reg := newTestServer(t, time.Hour)
	resp, err := http.Get(srv.URL + "/events?id=s1")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
	var sess *Session
	waitFor(t, "session registration", func() bool {
		sess = reg.Get("s1")
		return sess != nil
	})
	if _, err := sess.Write([]byte("hello")); err != nil {
		t.Fatalf("session Write: %v", err)
	}
	sess.Close() // ends the stream so ReadAll returns
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	want := "data: " + base64.StdEncoding.EncodeToString([]byte("hello")) + "\n\n"
	if !strings.Contains(string(body), want) {
		t.Fatalf("SSE body %q does not contain frame %q", body, want)
	}
}

func TestServerHeartbeatEmitted(t *testing.T) {
	t.Parallel()
	srv, reg := newTestServer(t, 10*time.Millisecond)
	resp, err := http.Get(srv.URL + "/events?id=hb")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	var sess *Session
	waitFor(t, "session registration", func() bool {
		sess = reg.Get("hb")
		return sess != nil
	})
	time.Sleep(100 * time.Millisecond) // 10x the heartbeat interval
	sess.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !strings.Contains(string(body), ": ka\n\n") {
		t.Fatalf("SSE body %q contains no heartbeat comment", body)
	}
}

func TestServerUpValidation(t *testing.T) {
	t.Parallel()
	srv, reg := newTestServer(t, time.Hour)
	reg.Replace(NewSession("s"))
	if code := postUp(t, srv.URL, "s", 0, []byte("first")); code != http.StatusOK {
		t.Fatalf("seed POST: got %d, want 200", code)
	}
	tests := []struct {
		name      string
		sessionID string
		seqHeader string
		wantCode  int
	}{
		{"missing session header", "", "1", http.StatusBadRequest},
		{"unknown session", "nope", "0", http.StatusConflict},
		{"seq gap", "s", "5", http.StatusConflict},
		{"duplicate old seq discarded", "s", "0", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/up", strings.NewReader("x"))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			if tt.sessionID != "" {
				req.Header.Set("X-SSET-Session", tt.sessionID)
			}
			req.Header.Set("X-SSET-Seq", tt.seqHeader)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST /up: %v", err)
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != tt.wantCode {
				t.Fatalf("got %d, want %d", resp.StatusCode, tt.wantCode)
			}
		})
	}
	// Bad seq header separately (needs a non-numeric value).
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/up", strings.NewReader("x"))
	req.Header.Set("X-SSET-Session", "s")
	req.Header.Set("X-SSET-Seq", "abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /up: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad seq header: got %d, want 400", resp.StatusCode)
	}
	// Duplicate seq must not have been delivered to the session.
	sess := reg.Get("s")
	sess.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 16)
	n, _ := sess.Read(buf)
	if string(buf[:n]) != "first" {
		t.Fatalf("read %q, want %q (duplicate seq must be discarded)", buf[:n], "first")
	}
	// Nothing more was delivered: the next read times out.
	_, err = sess.Read(buf)
	var nerr net.Error
	if !errors.As(err, &nerr) || !nerr.Timeout() {
		t.Fatalf("second read: got %v, want i/o timeout (no duplicate bytes)", err)
	}
}

func TestServerUpBodyTooLarge(t *testing.T) {
	t.Parallel()
	srv, reg := newTestServer(t, time.Hour)
	reg.Replace(NewSession("s"))
	body := make([]byte, maxUpBody+1)
	if code := postUp(t, srv.URL, "s", 0, body); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("got %d, want 413", code)
	}
	// Session still usable afterwards: next seq accepted.
	if code := postUp(t, srv.URL, "s", 0, []byte("ok")); code != http.StatusOK {
		t.Fatalf("after 413: got %d, want 200", code)
	}
}

func TestServerSessionReplacement(t *testing.T) {
	t.Parallel()
	srv, reg := newTestServer(t, time.Hour)
	resp1, err := http.Get(srv.URL + "/events?id=x")
	if err != nil {
		t.Fatalf("GET 1: %v", err)
	}
	defer resp1.Body.Close()
	var s1 *Session
	waitFor(t, "first session", func() bool {
		s1 = reg.Get("x")
		return s1 != nil
	})
	resp2, err := http.Get(srv.URL + "/events?id=x")
	if err != nil {
		t.Fatalf("GET 2: %v", err)
	}
	defer resp2.Body.Close()
	// New ID replaces stale session: registry swaps, old session dies.
	waitFor(t, "replacement", func() bool { return reg.Get("x") != s1 })
	// Old SSE stream terminates (its session was closed).
	eof := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(resp1.Body)
		eof <- err
	}()
	select {
	case err := <-eof:
		if err != nil {
			t.Fatalf("old stream read: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old SSE stream did not terminate after replacement")
	}
	// Old session reads EOF.
	if _, err := s1.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("stale session Read: got %v, want EOF", err)
	}
}

func TestServerReadEOFOnClose(t *testing.T) {
	t.Parallel()
	sess := NewSession("direct")
	// Upstream bytes arrive via push (POST /up), downstream via Write.
	if code := sess.push(0, []byte("pending")); code != 200 {
		t.Fatalf("push: got %d, want 200", code)
	}
	sess.Close()
	// Buffered bytes drain first, then EOF.
	buf := make([]byte, 16)
	n, err := sess.Read(buf)
	if string(buf[:n]) != "pending" {
		t.Fatalf("read %q, want buffered %q", buf[:n], "pending")
	}
	for err == nil {
		_, err = sess.Read(buf)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("got %v, want io.EOF after close", err)
	}
}

func TestServerReadDeadlineTimeout(t *testing.T) {
	t.Parallel()
	sess := NewSession("direct")
	sess.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	start := time.Now()
	_, err := sess.Read(make([]byte, 1))
	var nerr net.Error
	if !errors.As(err, &nerr) || !nerr.Timeout() {
		t.Fatalf("got %v, want net.Error timeout", err)
	}
	if !strings.Contains(err.Error(), "i/o timeout") {
		t.Fatalf("error %q, want it to say i/o timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("read deadline took %v, want prompt expiry", elapsed)
	}
}

func TestServerPushWriteTimeout(t *testing.T) {
	t.Parallel()
	sess := NewSession("stuck")
	sess.WriteTimeout = 20 * time.Millisecond
	// Nobody reads: the up pipe fills (256 KiB cap) and push must give
	// up with 409 = session death, not block the handler forever (spec:
	// every non-SSE HTTP request is short-lived).
	body := make([]byte, 64<<10)
	code := 200
	start := time.Now()
	for i := 0; i < 10 && code == 200; i++ {
		code = sess.push(uint64(i), body)
	}
	if code != 409 {
		t.Fatalf("push into a full pipe: got %d, want 409", code)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("push took %v, want expiry near the write timeout", elapsed)
	}
	// Expiry kills the session: buffered bytes drain, then EOF.
	buf := make([]byte, 4<<10)
	var err error
	for err == nil {
		_, err = sess.Read(buf)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Read after push timeout: got %v, want EOF (session dead)", err)
	}
}
