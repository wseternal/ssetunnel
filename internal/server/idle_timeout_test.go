package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/agent"
	"github.com/wseternal/ssetunnel/internal/connect"
)

// TestHTTPServer_IdleTimeoutStability verifies that the agent session
// survives well past the server's HTTP idle-connection timeout. Without
// an explicit IdleTimeout, Go defaults to 60 s and silently closes
// idle TCP connections; the agent's transport then reuses a stale
// connection for POST /up, gets EOF, and the yamux session dies.
func TestHTTPServer_IdleTimeoutStability(t *testing.T) {
	srv := NewServer(50 * time.Millisecond)

	// Build the HTTP server with a short IdleTimeout to reproduce the
	// production default-60s bug in a fast test.
	httpSrv := srv.NewHTTPServer("127.0.0.1:0")
	httpSrv.IdleTimeout = 500 * time.Millisecond

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go httpSrv.Serve(ln)
	defer httpSrv.Close()

	serverURL := fmt.Sprintf("http://%s", ln.Addr().String())

	// Echo target.
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				io.Copy(conn, conn)
			}(c)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect agent.
	ag := &agent.Agent{
		ServerURL:  serverURL,
		Target:     echoLn.Addr().String(),
		MaxBackoff: 50 * time.Millisecond,
	}
	go ag.Run(ctx)

	// Wait for the agent to register.
	var sessID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ids := srv.Reg.IDs(); len(ids) > 0 {
			sessID = ids[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sessID == "" {
		t.Fatal("agent did not register within 5s")
	}

	// Verify the tunnel works with a round-trip.
	doAgentRoundTrip(t, serverURL, []byte("before idle"))

	// Wait well past the idle timeout (500ms). The agent's yamux
	// keepalive fires every 30s, so between keepalives the POST
	// connection pool goes idle. We wait 2s to be sure.
	time.Sleep(2 * time.Second)

	// The session must NOT have been replaced (no spurious reconnect).
	ids := srv.Reg.IDs()
	if len(ids) == 0 {
		t.Fatal("agent session gone after idle wait")
	}
	if ids[0] != sessID {
		t.Fatalf("session replaced during idle: was %s, now %s", sessID, ids[0])
	}

	// The tunnel must still work.
	doAgentRoundTrip(t, serverURL, []byte("after idle"))

	// Wait again past the idle timeout to ensure stability.
	time.Sleep(2 * time.Second)

	ids = srv.Reg.IDs()
	if len(ids) == 0 || ids[0] != sessID {
		t.Fatalf("session unstable across idle periods: was %s, now %v", sessID, ids)
	}
	doAgentRoundTrip(t, serverURL, []byte("still working"))
}

// doAgentRoundTrip uses the connect client to dial the server and verifies a byte-exact echo.
func doAgentRoundTrip(t *testing.T, serverURL string, data []byte) {
	t.Helper()
	localLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen local: %v", err)
	}
	defer localLn.Close()

	client := connect.NewClient(serverURL, "", "", "")
	client.BatchSize = 64 << 10
	go client.ServeListener(context.Background(), localLn)

	c, err := net.Dial("tcp", localLn.Addr().String())
	if err != nil {
		t.Fatalf("dial local: %v", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))

	go func() { c.Write(data) }()
	got := make([]byte, len(data))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("echo: want %q, got %q", data, got)
	}
}

// TestHTTPServer_HasIdleTimeout verifies that NewHTTPServer sets an
// explicit IdleTimeout so Go's 60-second default does not silently
// close idle connections and break the agent's POST pool.
func TestHTTPServer_HasIdleTimeout(t *testing.T) {
	srv := NewServer(15 * time.Second)
	httpSrv := srv.NewHTTPServer(":0")
	if httpSrv.IdleTimeout == 0 {
		t.Fatal("NewHTTPServer must set a non-zero IdleTimeout to prevent " +
			"Go's 60s default from closing idle agent connections")
	}
	if httpSrv.IdleTimeout < 2*time.Minute {
		t.Fatalf("IdleTimeout = %v; want ≥ 2m (must exceed yamux 30s keepalive "+
			"by a wide margin)", httpSrv.IdleTimeout)
	}
}

// TestHTTPServer_PostAfterIdleTimeout verifies that a POST on a
// connection that outlived the idle timeout still succeeds (the
// transport should detect the stale connection and open a fresh one).
func TestHTTPServer_PostAfterIdleTimeout(t *testing.T) {
	reg := NewRegistry()
	h := NewHandler(reg, time.Hour)
	srv := &http.Server{
		Handler:     h,
		IdleTimeout: 200 * time.Millisecond,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go srv.Serve(ln)
	defer srv.Close()

	base := fmt.Sprintf("http://%s", ln.Addr().String())

	// Register a session.
	reg.Replace(NewSession("idle-post"))

	// Use a client with keep-alives (default) to pool connections.
	client := &http.Client{}

	// First POST: fresh connection, should succeed.
	doPost(t, client, base, "idle-post", 0)

	// Wait past the idle timeout so the server closes the pooled conn.
	time.Sleep(500 * time.Millisecond)

	// Second POST: the transport reuses the (now stale) connection.
	// With the fix (long IdleTimeout), this succeeds on a fresh conn.
	// Without the fix (short IdleTimeout), this may get EOF.
	doPost(t, client, base, "idle-post", 1)
}

func doPost(t *testing.T, client *http.Client, base, sessID string, seq uint64) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/up", bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-SSET-Session", sessID)
	req.Header.Set("X-SSET-Seq", strconv.FormatUint(seq, 10))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /up seq %d: %v", seq, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /up seq %d: got %d, want 200", seq, resp.StatusCode)
	}
}
