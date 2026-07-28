package server

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// getEvents opens the SSE stream, optionally advertising agent caps, and
// returns the live response (body closes at test cleanup).
func getEvents(t *testing.T, baseURL, id, caps string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/events?id="+id, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if caps != "" {
		req.Header.Set("X-SSET-Caps", caps)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// postUpFull issues one upstream POST with optional extra headers.
func postUpFull(t *testing.T, baseURL, sessionID string, seq uint64, body []byte, hdr map[string]string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/up", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-SSET-Session", sessionID)
	req.Header.Set("X-SSET-Seq", strconv.FormatUint(seq, 10))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /up: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func gzipBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestServerEventsCapsAdvertised(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, time.Hour)
	resp := getEvents(t, srv.URL, "caps", "")
	if got := resp.Header.Get("X-SSET-Caps"); got != "concurrency=4;batch=1048576;gzip" {
		t.Fatalf("X-SSET-Caps = %q, want %q", got, "concurrency=4;batch=1048576;gzip")
	}
}

// TestServerCapsNegotiation: a reorder window exists only when the agent
// negotiated concurrency>1; everything else (absent, malformed,
// concurrency=1) falls back to the legacy gap-rejecting path.
func TestServerCapsNegotiation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		caps       string // request header value; "" = absent
		wantWindow bool
	}{
		{"absent header", "", false},
		{"malformed header", ";;;", false},
		{"malformed value", "concurrency=x", false},
		{"concurrency 1", "concurrency=1", false},
		{"concurrency 0", "concurrency=0", false},
		{"concurrency 4", "concurrency=4;batch=65536;gzip", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, reg := newTestServer(t, time.Hour)
			getEvents(t, srv.URL, "neg", tt.caps)
			var sess *Session
			waitFor(t, "session registration", func() bool {
				sess = reg.Get("neg")
				return sess != nil
			})
			// Out-of-order POST (seq 1 before seq 0): buffered by the
			// window (200) or rejected as a gap by the legacy path (409).
			code := postUp(t, srv.URL, "neg", 1, []byte("x"))
			if tt.wantWindow && code != http.StatusOK {
				t.Fatalf("negotiated session, out-of-order POST: got %d, want 200", code)
			}
			if !tt.wantWindow && code != http.StatusConflict {
				t.Fatalf("legacy session, out-of-order POST: got %d, want 409", code)
			}
		})
	}
}

// TestServerShuffledPostsReassemble: concurrent POSTs arriving in a
// deterministic shuffled order (release-gate hook, no sleeps) reassemble
// byte-exact through sess.Read.
func TestServerShuffledPostsReassemble(t *testing.T) {
	t.Parallel()
	const n = 8
	reg := NewRegistry()
	h := NewHandler(reg, time.Hour)
	gates := make(map[uint64]chan struct{}, n)
	for i := 0; i < n; i++ {
		gates[uint64(i)] = make(chan struct{})
	}
	var arrived atomic.Int64
	h.OnUpPush = func(seq uint64) <-chan struct{} {
		arrived.Add(1)
		return gates[seq]
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	getEvents(t, srv.URL, "shuf", "concurrency=4")
	var sess *Session
	waitFor(t, "session registration", func() bool {
		sess = reg.Get("shuf")
		return sess != nil
	})

	payloads := make([][]byte, n)
	var want bytes.Buffer
	for i := 0; i < n; i++ {
		payloads[i] = []byte(fmt.Sprintf("batch-%d:%s", i, strings.Repeat(string(rune('a'+i)), 1000)))
		want.Write(payloads[i])
	}

	// Fire all POSTs concurrently; each parks on its gate before push.
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(seq uint64) {
			defer wg.Done()
			codes <- postUp(t, srv.URL, "shuf", seq, payloads[seq])
		}(uint64(i))
	}
	waitFor(t, "all POSTs gated", func() bool { return arrived.Load() == n })
	// Release in a shuffled order: pushes hit the window out of order.
	for _, seq := range []uint64{3, 0, 5, 1, 7, 2, 6, 4} {
		close(gates[seq])
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != http.StatusOK {
			t.Fatalf("POST: got %d, want 200", code)
		}
	}

	sess.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, want.Len())
	if _, err := io.ReadFull(sess, got); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatal("reassembled bytes differ from seq-order payloads")
	}
}

// TestServerWindowDuplicateDropped: a duplicate seq on a window session
// is acked (200) but delivers no bytes.
func TestServerWindowDuplicateDropped(t *testing.T) {
	t.Parallel()
	srv, reg := newTestServer(t, time.Hour)
	getEvents(t, srv.URL, "dup", "concurrency=4")
	var sess *Session
	waitFor(t, "session registration", func() bool {
		sess = reg.Get("dup")
		return sess != nil
	})
	if code := postUp(t, srv.URL, "dup", 0, []byte("first")); code != http.StatusOK {
		t.Fatalf("POST seq 0: got %d, want 200", code)
	}
	if code := postUp(t, srv.URL, "dup", 0, []byte("first")); code != http.StatusOK {
		t.Fatalf("duplicate POST: got %d, want 200", code)
	}
	sess.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 16)
	n, _ := sess.Read(buf)
	if string(buf[:n]) != "first" {
		t.Fatalf("read %q, want %q", buf[:n], "first")
	}
	_, err := sess.Read(buf)
	var nerr net.Error
	if !errors.As(err, &nerr) || !nerr.Timeout() {
		t.Fatalf("second read: got %v, want i/o timeout (no duplicate bytes)", err)
	}
}

// TestServerWindowGapTimeout: an unhealed gap past the session's
// GapTimeout fails the next POST with 409 and kills the session.
func TestServerWindowGapTimeout(t *testing.T) {
	t.Parallel()
	srv, reg := newTestServer(t, time.Hour)
	// Arm the window with a tiny GapTimeout (Session field) before any
	// handler touches the session — same state /events negotiation sets.
	sess := NewSession("gap")
	sess.GapTimeout = 10 * time.Millisecond
	sess.enableWindow()
	reg.Replace(sess)
	if code := postUp(t, srv.URL, "gap", 1, []byte("late")); code != http.StatusOK {
		t.Fatalf("POST seq 1: got %d, want 200 (buffered)", code)
	}
	time.Sleep(time.Second) // 100x the gap timeout; seq 0 never arrives
	if code := postUp(t, srv.URL, "gap", 2, []byte("later")); code != http.StatusConflict {
		t.Fatalf("POST past gap timeout: got %d, want 409", code)
	}
	if _, err := sess.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("Read after gap timeout: got %v, want EOF (session dead)", err)
	}
}

func TestServerUpGzipFlag(t *testing.T) {
	t.Parallel()
	// Compressible payload: gzip must shrink it so the test is meaningful.
	raw := []byte(strings.Repeat("the quick brown fox jumps over the lazy dog. ", 2000))
	tests := []struct {
		name     string
		caps     string // agent caps on /events; "" = legacy session
		flags    string
		body     []byte
		wantCode int
		wantRead []byte // non-nil: session must read these exact bytes
	}{
		{"gzip round trip", "concurrency=4;gzip", "gzip", gzipBytes(t, raw), http.StatusOK, raw},
		{"unknown flag", "concurrency=4;gzip", "snappy", []byte("x"), http.StatusBadRequest, nil},
		{"gzip on legacy session", "", "gzip", gzipBytes(t, raw), http.StatusBadRequest, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, reg := newTestServer(t, time.Hour)
			getEvents(t, srv.URL, "gz", tt.caps)
			var sess *Session
			waitFor(t, "session registration", func() bool {
				sess = reg.Get("gz")
				return sess != nil
			})
			hdr := map[string]string{}
			if tt.flags != "" {
				hdr["X-SSET-Flags"] = tt.flags
			}
			if code := postUpFull(t, srv.URL, "gz", 0, tt.body, hdr); code != tt.wantCode {
				t.Fatalf("POST: got %d, want %d", code, tt.wantCode)
			}
			if tt.wantRead != nil {
				sess.SetReadDeadline(time.Now().Add(2 * time.Second))
				got := make([]byte, len(tt.wantRead))
				if _, err := io.ReadFull(sess, got); err != nil {
					t.Fatalf("Read: %v", err)
				}
				if !bytes.Equal(got, tt.wantRead) {
					t.Fatal("gunzipped bytes differ from original payload")
				}
			}
		})
	}
}

// TestServerUpBodyBoundary: exactly the 1 MiB batch ceiling and exactly
// the defensive cap are accepted; one byte past the cap is 413.
func TestServerUpBodyBoundary(t *testing.T) {
	t.Parallel()
	srv, reg := newTestServer(t, time.Hour)
	reg.Replace(NewSession("s"))
	// Drain the session like yamux would: a body bigger than the up pipe
	// (256 KiB) must flow through instead of stalling the push.
	go io.Copy(io.Discard, reg.Get("s"))
	if code := postUp(t, srv.URL, "s", 0, make([]byte, 1<<20)); code != http.StatusOK {
		t.Fatalf("1 MiB body: got %d, want 200", code)
	}
	if code := postUp(t, srv.URL, "s", 1, make([]byte, maxUpBody)); code != http.StatusOK {
		t.Fatalf("maxUpBody body: got %d, want 200", code)
	}
	if code := postUp(t, srv.URL, "s", 2, make([]byte, maxUpBody+1)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("maxUpBody+1 body: got %d, want 413", code)
	}
	// Session still usable afterwards: next seq accepted.
	if code := postUp(t, srv.URL, "s", 2, []byte("ok")); code != http.StatusOK {
		t.Fatalf("after 413: got %d, want 200", code)
	}
}

func TestServerProbe(t *testing.T) {
	t.Parallel()
	srv, reg := newTestServer(t, time.Hour)
	// Small body: read-and-discard, 200, and NO session is registered.
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/probe", strings.NewReader("ping"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /probe: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /probe: got %d, want 200", resp.StatusCode)
	}
	if ids := reg.IDs(); len(ids) != 0 {
		t.Fatalf("/probe registered sessions %v, want none", ids)
	}
	// 2 MiB cap: exact cap accepted, one byte past → 413.
	if code := probePost(t, srv.URL, make([]byte, maxProbeBody)); code != http.StatusOK {
		t.Fatalf("POST /probe maxProbeBody: got %d, want 200", code)
	}
	if code := probePost(t, srv.URL, make([]byte, maxProbeBody+1)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /probe maxProbeBody+1: got %d, want 413", code)
	}
	// Wrong method → 405.
	resp, err = http.Get(srv.URL + "/probe")
	if err != nil {
		t.Fatalf("GET /probe: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /probe: got %d, want 405", resp.StatusCode)
	}
}

func probePost(t *testing.T, baseURL string, body []byte) int {
	t.Helper()
	resp, err := http.Post(baseURL+"/probe", "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /probe: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
