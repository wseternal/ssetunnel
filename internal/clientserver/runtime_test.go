package clientserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/clientserver"
)

// uiFixture is a real clientserver against a tunnel stub that holds /events
// open so a started agent stays alive. connected/disconnected signal the
// agent's SSE attachment lifecycle; serveDone closes after Serve returns.
type uiFixture struct {
	base         string
	secret       string
	connected    chan struct{}
	disconnected chan struct{}
	stop         context.CancelFunc
	serveDone    chan struct{}
	cleanup      func()
}

func startUIServer(t *testing.T) uiFixture {
	t.Helper()
	// Short HOME: the agent's module IPC unix socket path must stay under
	// macOS's 104-byte sun_path limit, which t.TempDir()'s test-name path
	// plus .ssetunnel/agent/module.sock would exceed.
	home, err := os.MkdirTemp("", "ssetui")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)

	connected := make(chan struct{}, 16)
	disconnected := make(chan struct{}, 16)
	tunnel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case connected <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			select {
			case disconnected <- struct{}{}:
			default:
			}
			return
		}
		http.NotFound(w, r)
	}))

	if err := auth.SaveSession(tunnel.URL, "test-token", "tester", "admin", "", time.Time{}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	srv, err := clientserver.New(clientserver.Config{})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	url := srv.URL()
	idx := strings.IndexByte(url, '#')
	if idx < 0 {
		t.Fatalf("server URL has no secret fragment: %s", url)
	}
	f := uiFixture{
		base:         strings.TrimRight(url[:idx], "/"),
		secret:       url[idx+1:],
		connected:    connected,
		disconnected: disconnected,
		serveDone:    make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	f.stop = cancel
	go func() {
		srv.Serve(ctx)
		close(f.serveDone)
	}()

	// Wait until the API answers.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(f.base + "/api/agent/status")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	f.cleanup = func() {
		cancel()
		select {
		case <-f.serveDone:
		case <-time.After(15 * time.Second):
			t.Error("Serve did not return within 15s of context cancel")
		}
		tunnel.Close()
	}
	return f
}

func apiCall(t *testing.T, client *http.Client, method, url, secret string, body any) int {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func waitRunning(t *testing.T, client *http.Client, base, secret string, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, _ := http.NewRequest("GET", base+"/api/agent/status", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		var st struct {
			Running bool `json:"running"`
		}
		json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		if st.Running == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent running=%v never reached (want %v)", st.Running, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

var agentCfg = map[string]any{
	"target":   "127.0.0.1:1",
	"agent_id": "lifecycle-test",
}

// TestAgentRestartWhileRunning pins C1: start() must not deadlock when an
// agent is already running. Before the fix it held the runtime mutex across
// the wait for the old goroutine, which needed that mutex to exit — the
// restart request, every later status poll, and shutdown all hung forever.
func TestAgentRestartWhileRunning(t *testing.T) {
	f := startUIServer(t)
	defer f.cleanup()

	client := &http.Client{Timeout: 5 * time.Second}

	if code := apiCall(t, client, "POST", f.base+"/api/agent/start", f.secret, agentCfg); code != http.StatusNoContent {
		t.Fatalf("start: got %d, want 204", code)
	}
	select {
	case <-f.connected:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never connected to the tunnel stub")
	}
	waitRunning(t, client, f.base, f.secret, true)

	// Restart with a live agent: must return promptly (pre-fix: deadlock).
	if code := apiCall(t, client, "POST", f.base+"/api/agent/restart", f.secret, agentCfg); code != http.StatusNoContent {
		t.Fatalf("restart: got %d, want 204", code)
	}
	// Status must still answer after the restart.
	waitRunning(t, client, f.base, f.secret, true)

	if code := apiCall(t, client, "POST", f.base+"/api/agent/stop", f.secret, nil); code != http.StatusNoContent {
		t.Fatalf("stop: got %d, want 204", code)
	}
	waitRunning(t, client, f.base, f.secret, false)
}

// TestAgentConcurrentStopStart pins C2: after concurrent stop/start the
// lifecycle must remain controllable — a stop reaches a stable stopped
// state and a later start works again. (opMu serializes the transitions;
// the generation guard in stop is defense-in-depth, so this test pins
// controllability, not the raw race window.)
func TestAgentConcurrentStopStart(t *testing.T) {
	f := startUIServer(t)
	defer f.cleanup()

	client := &http.Client{Timeout: 5 * time.Second}

	if code := apiCall(t, client, "POST", f.base+"/api/agent/start", f.secret, agentCfg); code != http.StatusNoContent {
		t.Fatalf("start: got %d, want 204", code)
	}
	select {
	case <-f.connected:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never connected to the tunnel stub")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// t.Errorf (not Fatalf) here: FailNow must run on the test goroutine.
		req, err := http.NewRequest("POST", f.base+"/api/agent/stop", nil)
		if err != nil {
			t.Errorf("concurrent stop request: %v", err)
			return
		}
		req.Header.Set("Authorization", "Bearer "+f.secret)
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("concurrent stop: %v", err)
			return
		}
		resp.Body.Close()
	}()
	apiCall(t, client, "POST", f.base+"/api/agent/start", f.secret, agentCfg)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent stop never returned")
	}

	// Whatever the interleaving, the lifecycle must remain controllable.
	if code := apiCall(t, client, "POST", f.base+"/api/agent/stop", f.secret, nil); code != http.StatusNoContent {
		t.Fatalf("stop after race: got %d, want 204", code)
	}
	waitRunning(t, client, f.base, f.secret, false)
	if code := apiCall(t, client, "POST", f.base+"/api/agent/start", f.secret, agentCfg); code != http.StatusNoContent {
		t.Fatalf("start after race: got %d, want 204", code)
	}
	waitRunning(t, client, f.base, f.secret, true)
	if code := apiCall(t, client, "POST", f.base+"/api/agent/stop", f.secret, nil); code != http.StatusNoContent {
		t.Fatalf("final stop: got %d, want 204", code)
	}
	waitRunning(t, client, f.base, f.secret, false)
}

// TestShutdownWithRunningAgent pins W1: cancelling the server context must
// tear down the owned agent (its tunnel attachment drops) and Serve must
// return only after teardown, within a bounded time.
func TestShutdownWithRunningAgent(t *testing.T) {
	f := startUIServer(t)
	defer f.cleanup()

	client := &http.Client{Timeout: 5 * time.Second}
	if code := apiCall(t, client, "POST", f.base+"/api/agent/start", f.secret, agentCfg); code != http.StatusNoContent {
		t.Fatalf("start: got %d, want 204", code)
	}
	select {
	case <-f.connected:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never connected to the tunnel stub")
	}

	f.stop()
	select {
	case <-f.disconnected:
	case <-time.After(10 * time.Second):
		t.Fatal("agent tunnel attachment not torn down on shutdown")
	}
	select {
	case <-f.serveDone:
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return after teardown")
	}
}

// TestLogoutStopsOwnedRuntimes pins S5: the UI owns its agent and connects,
// so logout must tear them down instead of leaving them alive on a deleted
// credential until silent server-side expiry.
func TestLogoutStopsOwnedRuntimes(t *testing.T) {
	f := startUIServer(t)
	defer f.cleanup()

	client := &http.Client{Timeout: 30 * time.Second}
	if code := apiCall(t, client, "POST", f.base+"/api/agent/start", f.secret, agentCfg); code != http.StatusNoContent {
		t.Fatalf("start: got %d, want 204", code)
	}
	select {
	case <-f.connected:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never connected to the tunnel stub")
	}
	waitRunning(t, client, f.base, f.secret, true)

	if code := apiCall(t, client, "POST", f.base+"/api/logout", f.secret, nil); code != http.StatusNoContent {
		t.Fatalf("logout: got %d, want 204", code)
	}
	waitRunning(t, client, f.base, f.secret, false)
	select {
	case <-f.disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("logout did not tear down the agent's tunnel attachment")
	}
}
