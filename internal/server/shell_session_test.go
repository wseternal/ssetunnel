package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
)

func TestShellSessionRegistry_StoreLoadDelete(t *testing.T) {
	reg := NewShellSessionRegistry()

	ss := &ShellSession{
		id:           "test-1",
		agentID:      "agent-a",
		userID:       42,
		ring:         NewRingBuffer(64),
		done:         make(chan struct{}),
		createdAt:    time.Now(),
		lastActivity: time.Now(),
	}

	reg.Store(ss)
	if got := reg.Load("test-1"); got != ss {
		t.Fatalf("Load = %v, want %v", got, ss)
	}
	if got := reg.Load("nonexistent"); got != nil {
		t.Fatalf("Load nonexistent = %v, want nil", got)
	}

	reg.Delete("test-1")
	if got := reg.Load("test-1"); got != nil {
		t.Fatalf("after Delete, Load = %v, want nil", got)
	}
}

func TestShellSessionRegistry_FindByUserAgent(t *testing.T) {
	reg := NewShellSessionRegistry()

	ss1 := &ShellSession{
		id: "s1", agentID: "agent-a", userID: 1,
		ring: NewRingBuffer(64), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}
	ss2 := &ShellSession{
		id: "s2", agentID: "agent-b", userID: 2,
		ring: NewRingBuffer(64), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}
	reg.Store(ss1)
	reg.Store(ss2)

	// User 1 finds agent-a.
	if got := reg.FindByUserAgent(1, "agent-a"); got != ss1 {
		t.Fatalf("FindByUserAgent(1, agent-a) = %v, want ss1", got)
	}
	// User 1 cannot find agent-b.
	if got := reg.FindByUserAgent(1, "agent-b"); got != nil {
		t.Fatalf("FindByUserAgent(1, agent-b) = %v, want nil", got)
	}
	// Admin (userID < 0) finds any.
	if got := reg.FindByUserAgent(-1, "agent-b"); got != ss2 {
		t.Fatalf("FindByUserAgent(-1, agent-b) = %v, want ss2", got)
	}
	// Not found.
	if got := reg.FindByUserAgent(1, "agent-c"); got != nil {
		t.Fatalf("FindByUserAgent(1, agent-c) = %v, want nil", got)
	}
}

func TestShellSessionRegistry_CleanupIdle(t *testing.T) {
	reg := NewShellSessionRegistry()

	ss := &ShellSession{
		id: "idle-1", agentID: "agent-a", userID: 1,
		ring: NewRingBuffer(64), done: make(chan struct{}),
		createdAt:    time.Now(),
		lastActivity: time.Now().Add(-2 * time.Hour), // well past timeout
	}
	reg.Store(ss)

	// Cleanup with 1-hour timeout should remove it.
	reg.CleanupIdle(time.Hour)
	if got := reg.Load("idle-1"); got != nil {
		t.Fatalf("after CleanupIdle, Load = %v, want nil", got)
	}
}

func TestShellSessionRegistry_CleanupIdle_SkipsAttached(t *testing.T) {
	reg := NewShellSessionRegistry()

	ss := &ShellSession{
		id: "attached-1", agentID: "agent-a", userID: 1,
		ring: NewRingBuffer(64), done: make(chan struct{}),
		createdAt:    time.Now(),
		lastActivity: time.Now().Add(-2 * time.Hour),
	}
	// Simulate attached state.
	ss.sseWriter = &discardWriter{}
	reg.Store(ss)

	// Cleanup should NOT remove attached sessions.
	reg.CleanupIdle(time.Hour)
	if got := reg.Load("attached-1"); got == nil {
		t.Fatalf("CleanupIdle removed attached session, should have skipped")
	}
}

type discardWriter struct{}

func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestShellSessionRegistry_CloseAll(t *testing.T) {
	reg := NewShellSessionRegistry()

	ss := &ShellSession{
		id: "close-all", agentID: "agent-a", userID: 1,
		ring: NewRingBuffer(64), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}
	reg.Store(ss)
	reg.CloseAll()

	if got := reg.Load("close-all"); got != nil {
		t.Fatalf("after CloseAll, Load = %v, want nil", got)
	}
	if !ss.IsDead() {
		t.Fatalf("session should be dead after CloseAll")
	}
}

func TestShellSession_AttachClosedSession(t *testing.T) {
	ss := &ShellSession{
		id: "closed-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(64), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}
	ss.closed.Store(true)

	if _, err := ss.Attach(&discardWriter{}, nil, false); err == nil {
		t.Fatal("Attach on closed session should fail")
	}
}

func TestShellSession_DoubleAttach(t *testing.T) {
	ss := &ShellSession{
		id: "dbl-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(64), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}

	first := &discardWriter{}
	if _, err := ss.Attach(first, nil, false); err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	if _, err := ss.Attach(&discardWriter{}, nil, false); err == nil {
		t.Fatal("second Attach should fail (already attached)")
	}
	ss.detachWriter(first, false)
	// After detaching, should be able to Attach again.
	if _, err := ss.Attach(&discardWriter{}, nil, false); err != nil {
		t.Fatalf("Attach after detach: %v", err)
	}
}

// TestShellSession_AttachReplayBoundary covers what Attach does with buffered
// output after the ring has evicted older bytes. Eviction advances the head by
// an arbitrary count with no notion of message boundaries, so a replay can begin
// inside an ANSI sequence and the terminal renders the leftovers as permanent
// scrollback garbage ("1;31mABC" is what survives of "\x1b[1;31mABC"). The skip
// must apply only to output that was actually truncated: an overflowed buffer is
// drained here, so a wrongly-skipped byte is unrecoverable for later reattaches.
func TestShellSession_AttachReplayBoundary(t *testing.T) {
	tests := []struct {
		name   string
		cap    int
		writes []string
		want   string
	}{
		{
			// Cap 8: the "\x1b[" introducer is overwritten, leaving "1;31mABC".
			name:   "evicted CSI tail is skipped",
			cap:    8,
			writes: []string{"\x1b[1;31m", "ABC"},
			want:   "ABC",
		},
		{
			// Cap 8 with a 9-byte write: the retained head is the BEL that
			// terminated an evicted OSC.
			name:   "evicted sequence terminator is skipped",
			cap:    8,
			writes: []string{"X\x07AAAAAAAA"},
			want:   "AAAAAAAA",
		},
		{
			// A leading space is a CSI intermediate byte but far more often it
			// is ordinary indentation, so it must survive.
			name:   "evicted leading indentation is kept",
			cap:    8,
			writes: []string{"X hello!!"},
			want:   " hello!!",
		},
		{
			// No eviction: the head is a real write boundary and the replay must
			// be byte-exact even though "512M" looks like a truncated tail.
			name:   "un-evicted output is never trimmed",
			cap:    64,
			writes: []string{"512M\t./dir"},
			want:   "512M\t./dir",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ss := &ShellSession{
				id: "boundary-1", agentID: "a", userID: 1,
				ring: NewRingBuffer(tt.cap), done: make(chan struct{}),
				createdAt: time.Now(), lastActivity: time.Now(),
			}
			for _, w := range tt.writes {
				ss.ring.Write([]byte(w))
			}

			var buf bytes.Buffer
			if _, err := ss.Attach(&buf, &nopFlusher{}, false); err != nil {
				t.Fatalf("Attach: %v", err)
			}
			if got := string(decodeSSEFrames(t, buf.String())); got != tt.want {
				t.Fatalf("replay = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShellSession_AttachDrainsRingBuffer(t *testing.T) {
	ss := &ShellSession{
		id: "drain-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(256), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}

	// Pre-populate the ring buffer.
	ss.ring.Write([]byte("hello world"))
	if ss.ring.Len() != 11 {
		t.Fatalf("ring Len = %d, want 11", ss.ring.Len())
	}

	// Attach should drain the ring buffer.
	var buf bytes.Buffer
	f := &nopFlusher{}
	if _, err := ss.Attach(&buf, f, false); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if ss.ring.Len() != 0 {
		t.Fatalf("after Attach, ring Len = %d, want 0", ss.ring.Len())
	}
	if buf.Len() == 0 {
		t.Fatal("Attach should have written buffered data to writer")
	}
}

func TestShellSession_CloseIdempotent(t *testing.T) {
	ss := &ShellSession{
		id: "idem-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(64), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}

	if err := ss.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := ss.Close(); err != nil {
		t.Fatalf("second Close should be no-op, got: %v", err)
	}
	if !ss.IsDead() {
		t.Fatal("session should be dead after Close")
	}
}

func TestShellSession_DetachClearsWriter(t *testing.T) {
	ss := &ShellSession{
		id: "det-1", agentID: "a", userID: 1,
		ring: NewRingBuffer(64), done: make(chan struct{}),
		createdAt: time.Now(), lastActivity: time.Now(),
	}

	w := &discardWriter{}
	_, _ = ss.Attach(w, nil, false)
	if !ss.Attached() {
		t.Fatal("should be attached")
	}
	ss.detachWriter(w, false)
	if ss.Attached() {
		t.Fatal("should not be attached after detachWriter")
	}
}

type nopFlusher struct{}

func (f *nopFlusher) Flush() {}

func TestShellSessionListHandler_SortedByAgentID(t *testing.T) {
	reg := NewShellSessionRegistry()

	// Register shell sessions in deliberately non-alphabetical agent order.
	for _, aid := range []string{"zebra-agent", "alpha-agent", "mango-agent"} {
		ss := &ShellSession{
			id:           "sess-" + aid,
			agentID:      aid,
			userID:       1,
			ring:         NewRingBuffer(64),
			done:         make(chan struct{}),
			createdAt:    time.Now(),
			lastActivity: time.Now(),
		}
		reg.Store(ss)
	}

	h := &Handler{shellSessions: reg}
	handler := h.ShellSessionListHandler()

	// Inject admin user session into context.
	sessInfo := &auth.UserSessionInfo{UserID: 1, Role: "admin"}
	req := httptest.NewRequest("GET", "/shell/sessions", nil)
	ctx := context.WithValue(req.Context(), userSessionKey, sessInfo)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var sessions []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("want 3 sessions, got %d", len(sessions))
	}

	want := []string{"alpha-agent", "mango-agent", "zebra-agent"}
	for i, s := range sessions {
		got, _ := s["agent_id"].(string)
		if got != want[i] {
			t.Errorf("sessions[%d]: want agent_id=%q, got %q", i, want[i], got)
		}
	}
}

func TestSafeReplayStart(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want int
	}{
		{"empty", nil, 0},
		{"clean boundary", []byte("hello"), 0},
		{"plain text", []byte("ls -la\n"), 0},
		// BEL characters are consumed as leading noise.
		{"bel prefix", []byte{0x07, 0x07, 'A'}, 2},
		{"all bels", []byte{0x07, 0x07, 0x07}, 3},
		// CSI parameter bytes (0x30-0x3F) followed by a final byte (0x40-0x7E)
		// indicate a truncated escape sequence.
		{"csi with final", []byte("1;2;3m"), 6},
		{"csi sgr", []byte("38;2;255;0;0m"), 13},
		{"single param + final", []byte("0m"), 2},
		// Parameter bytes without a final byte: pass through.
		{"params no final", []byte("123"), 0},
		// BEL then CSI: BEL consumed, CSI skipped.
		{"bel then csi", []byte{0x07, '1', 'm'}, 3},
		// Non-parameter byte at start: clean boundary.
		{"space start", []byte("  indented"), 0},
		{"tab start", []byte("\tindented"), 0},
		// High bytes (UTF-8 continuation): passed through.
		{"utf8 start", []byte{0xC3, 0xA9}, 0},
		// Bare ESC byte (0x1B): not a CSI sequence, pass through.
		{"bare esc", []byte{0x1B}, 0},
		// ESC followed by non-CSI byte: pass through.
		{"esc then letter", []byte{0x1B, 'A'}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safeReplayStart(tt.data)
			if got != tt.want {
				t.Errorf("safeReplayStart(%q) = %d, want %d", tt.data, got, tt.want)
			}
		})
	}
}
