package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/testutil"
	"github.com/wseternal/ssetunnel/internal/transport"
)

// TestCompatCapsLessAgentNewServer is compat matrix quadrant 1 (cycle-2
// plan step 3): a cycle-1-mode agent (no caps request header, response
// caps ignored) against a cycle-2 server advertising full caps must get
// pure cycle-1 behavior — legacy no-window session, byte-exact echo.
func TestCompatCapsLessAgentNewServer(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	srv := httptest.NewServer(NewHandler(reg, time.Hour))
	t.Cleanup(srv.Close)

	c, err := transport.DialAgent(context.Background(), transport.Config{
		URL:         srv.URL,
		SessionID:   "old",
		MaxWait:     time.Millisecond,
		DisableCaps: true, // cycle-1 wire behavior
	})
	if err != nil {
		t.Fatalf("DialAgent: %v", err)
	}
	defer c.Close()
	var sess *Session
	waitFor(t, "session registration", func() bool {
		sess = reg.Get("old")
		return sess != nil
	})

	// Behavioral proof the server took the legacy no-window path for this
	// session: an out-of-order POST is a gap → 409 (a windowed session
	// would buffer it → 200).
	if code := postUp(t, srv.URL, "old", 99, []byte("x")); code != http.StatusConflict {
		t.Fatalf("out-of-order POST on caps-less session: got %d, want 409 (legacy path)", code)
	}

	// 1 MiB patterned echo, upstream direction byte-exact.
	payload := pattern(1 << 20)
	go func() { c.Write(payload) }()
	got := make([]byte, len(payload))
	sess.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(sess, got); err != nil {
		t.Fatalf("session Read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("upstream 1 MiB not byte-exact")
	}

	// Downstream direction byte-exact.
	if _, err := sess.Write(payload); err != nil {
		t.Fatalf("session Write: %v", err)
	}
	got2 := make([]byte, len(payload))
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(c, got2); err != nil {
		t.Fatalf("conn Read: %v", err)
	}
	if !bytes.Equal(got2, payload) {
		t.Fatal("downstream 1 MiB not byte-exact")
	}
}

// TestMiddleboxStripHeaders: the middlebox knob models a header-stripping
// proxy — the server's caps advertisement never reaches the agent.
func TestMiddleboxStripHeaders(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	upstream := httptest.NewServer(NewHandler(reg, time.Hour))
	t.Cleanup(upstream.Close)
	mb, err := testutil.StartMiddlebox(upstream.URL, testutil.MiddleboxConfig{
		StripHeaders: []string{"X-SSET-Caps"},
	})
	if err != nil {
		t.Fatalf("StartMiddlebox: %v", err)
	}
	defer mb.Close()

	resp, err := http.Get(mb.URL + "/events?id=strip")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-SSET-Caps"); got != "" {
		t.Fatalf("X-SSET-Caps = %q, want stripped (empty)", got)
	}
	// Other headers survive stripping.
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
}

// recordedUp captures one /up request's wire form (flags + raw body).
type recordedUp struct {
	flags string
	body  []byte
}

// upRecorder is middleware recording every /up POST's wire form.
type upRecorder struct {
	mu    sync.Mutex
	posts []recordedUp
}

func (rec *upRecorder) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/up" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read body", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			rec.mu.Lock()
			rec.posts = append(rec.posts, recordedUp{r.Header.Get("X-SSET-Flags"), body})
			rec.mu.Unlock()
		}
		next.ServeHTTP(w, r)
	})
}

func (rec *upRecorder) snapshot() []recordedUp {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]recordedUp(nil), rec.posts...)
}

// setupStrippedCaps builds agent → StripHeaders middlebox → recording
// handlers: the agent never sees the server's caps advertisement.
func setupStrippedCaps(t *testing.T, sessionID string) (*transport.Conn, *Session, *upRecorder) {
	t.Helper()
	reg := NewRegistry()
	rec := &upRecorder{}
	ts := httptest.NewServer(rec.wrap(NewHandler(reg, time.Hour)))
	t.Cleanup(ts.Close)
	mb, err := testutil.StartMiddlebox(ts.URL, testutil.MiddleboxConfig{
		StripHeaders: []string{"X-SSET-Caps"},
	})
	if err != nil {
		t.Fatalf("StartMiddlebox: %v", err)
	}
	t.Cleanup(mb.Close)
	c, err := transport.DialAgent(context.Background(), transport.Config{
		URL:          mb.URL,
		SessionID:    sessionID,
		MaxWait:      time.Millisecond,
		MaxBatchSize: 64 << 10,
		Concurrency:  4,
		Compress:     true,
	})
	if err != nil {
		t.Fatalf("DialAgent: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	var sess *Session
	waitFor(t, "session registration", func() bool {
		sess = reg.Get(sessionID)
		return sess != nil
	})
	return c, sess, rec
}

// TestCompatNewAgentStrippedServer is compat quadrant 2 (cycle-2 plan
// step 5): a full-caps agent whose server's caps advertisement is
// stripped must fail closed to the cycle-1 profile — serial POSTs at
// DefaultMaxBatchSize, byte-exact delivery.
func TestCompatNewAgentStrippedServer(t *testing.T) {
	t.Parallel()
	c, sess, rec := setupStrippedCaps(t, "stripped")
	payload := pattern(1 << 20)
	go func() { c.Write(payload) }()
	got := make([]byte, len(payload))
	sess.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(sess, got); err != nil {
		t.Fatalf("session Read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("1 MiB through stripped middlebox not byte-exact")
	}
	posts := rec.snapshot()
	if len(posts) != 16 {
		t.Fatalf("POST count = %d, want 16 (1 MiB / 64 KiB agent-requested batch)", len(posts))
	}
	for i, p := range posts {
		if len(p.body) > 64<<10 {
			t.Fatalf("POST %d body = %d bytes, want <= 64 KiB (agent-requested batch)", i, len(p.body))
		}
	}
}

// TestCompatGzipStrippedServer: a compress-enabled agent behind a
// caps-stripping proxy must never send the gzip flag — every body raw.
func TestCompatGzipStrippedServer(t *testing.T) {
	t.Parallel()
	c, sess, rec := setupStrippedCaps(t, "gzstrip")
	payload := bytes.Repeat([]byte("compressible payload for the wire. "), 8000)[:256<<10]
	go func() { c.Write(payload) }()
	got := make([]byte, len(payload))
	sess.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(sess, got); err != nil {
		t.Fatalf("session Read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("payload through stripped middlebox not byte-exact")
	}
	posts := rec.snapshot()
	if len(posts) == 0 {
		t.Fatal("no POSTs recorded")
	}
	var wire int
	for i, p := range posts {
		if p.flags != "" {
			t.Fatalf("POST %d X-SSET-Flags = %q, want none on a stripped session", i, p.flags)
		}
		wire += len(p.body)
	}
	if wire != len(payload) {
		t.Fatalf("raw wire bytes = %d, want %d (no compression)", wire, len(payload))
	}
}
