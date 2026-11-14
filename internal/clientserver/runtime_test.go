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

// startUIServer boots a real clientserver against a tunnel stub that holds
// /events open so a started agent stays alive. Returns the base URL, the
// launch secret, a channel signalling an active agent connection, and a
// cleanup function.
func startUIServer(t *testing.T) (base, secret string, agentConnected chan struct{}, cleanup func()) {
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

	agentConnected = make(chan struct{}, 16)
	tunnel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case agentConnected <- struct{}{}:
			default:
			}
			<-r.Context().Done()
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
	base = strings.TrimRight(url[:idx], "/")
	secret = url[idx+1:]

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx) }()

	// Wait until the API answers.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(base + "/api/agent/status")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cleanup = func() {
		cancel()
		select {
		case <-serveDone:
		case <-time.After(15 * time.Second):
			t.Error("Serve did not return within 15s of context cancel")
		}
		tunnel.Close()
	}
	return base, secret, agentConnected, cleanup
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
	base, secret, connected, cleanup := startUIServer(t)
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}

	if code := apiCall(t, client, "POST", base+"/api/agent/start", secret, agentCfg); code != http.StatusNoContent {
		t.Fatalf("start: got %d, want 204", code)
	}
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never connected to the tunnel stub")
	}
	waitRunning(t, client, base, secret, true)

	// Restart with a live agent: must return promptly (pre-fix: deadlock).
	if code := apiCall(t, client, "POST", base+"/api/agent/restart", secret, agentCfg); code != http.StatusNoContent {
		t.Fatalf("restart: got %d, want 204", code)
	}
	// Status must still answer after the restart.
	waitRunning(t, client, base, secret, true)

	if code := apiCall(t, client, "POST", base+"/api/agent/stop", secret, nil); code != http.StatusNoContent {
		t.Fatalf("stop: got %d, want 204", code)
	}
	waitRunning(t, client, base, secret, false)
}

// TestAgentConcurrentStopStart pins C2: a stop racing a start must not nil
// the replacement run's cancel handle (which left an unstoppable ghost agent
// and a status that reported stopped).
func TestAgentConcurrentStopStart(t *testing.T) {
	base, secret, connected, cleanup := startUIServer(t)
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}

	if code := apiCall(t, client, "POST", base+"/api/agent/start", secret, agentCfg); code != http.StatusNoContent {
		t.Fatalf("start: got %d, want 204", code)
	}
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never connected to the tunnel stub")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		apiCall(t, client, "POST", base+"/api/agent/stop", secret, nil)
	}()
	apiCall(t, client, "POST", base+"/api/agent/start", secret, agentCfg)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent stop never returned")
	}

	// Whatever the interleaving, the lifecycle must remain controllable:
	// a stop reaches a stable stopped state and a later start works again.
	if code := apiCall(t, client, "POST", base+"/api/agent/stop", secret, nil); code != http.StatusNoContent {
		t.Fatalf("stop after race: got %d, want 204", code)
	}
	waitRunning(t, client, base, secret, false)
	if code := apiCall(t, client, "POST", base+"/api/agent/start", secret, agentCfg); code != http.StatusNoContent {
		t.Fatalf("start after race: got %d, want 204", code)
	}
	waitRunning(t, client, base, secret, true)
	if code := apiCall(t, client, "POST", base+"/api/agent/stop", secret, nil); code != http.StatusNoContent {
		t.Fatalf("final stop: got %d, want 204", code)
	}
	waitRunning(t, client, base, secret, false)
}

// TestShutdownWithRunningAgent pins W1: cancelling the server context must
// drain HTTP and tear down the owned agent within a bounded time.
func TestShutdownWithRunningAgent(t *testing.T) {
	base, secret, connected, cleanup := startUIServer(t)
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}
	if code := apiCall(t, client, "POST", base+"/api/agent/start", secret, agentCfg); code != http.StatusNoContent {
		t.Fatalf("start: got %d, want 204", code)
	}
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never connected to the tunnel stub")
	}
	// cleanup cancels the context and asserts Serve returns within 15s.
}

// TestLogoutStopsOwnedRuntimes pins S5: the UI owns its agent and connects,
// so logout must tear them down instead of leaving them alive on a deleted
// credential until silent server-side expiry.
func TestLogoutStopsOwnedRuntimes(t *testing.T) {
	base, secret, connected, cleanup := startUIServer(t)
	defer cleanup()

	client := &http.Client{Timeout: 30 * time.Second}
	if code := apiCall(t, client, "POST", base+"/api/agent/start", secret, agentCfg); code != http.StatusNoContent {
		t.Fatalf("start: got %d, want 204", code)
	}
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never connected to the tunnel stub")
	}
	waitRunning(t, client, base, secret, true)

	if code := apiCall(t, client, "POST", base+"/api/logout", secret, nil); code != http.StatusNoContent {
		t.Fatalf("logout: got %d, want 204", code)
	}
	waitRunning(t, client, base, secret, false)
}