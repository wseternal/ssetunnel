package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/agent"
	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/connect"
	"github.com/wseternal/ssetunnel/internal/consoleapi"
	"github.com/wseternal/ssetunnel/internal/server"
	"github.com/wseternal/ssetunnel/migrations"
	orcapostgres "github.com/visdomtech/orcacommon/postgres"
)

// ---------------------------------------------------------------------------
// Singleton test infrastructure (set up once in TestMain, shared by all tests)
// ---------------------------------------------------------------------------

var (
	// Shared echo target
	echoAddr string

	// No-auth server: agent connects freely, no token required.
	noAuthSrv   *server.Server
	noAuthAgent string // agent listener address

	// Auth server: agent must present a valid token.
	authSrv    *server.Server
	authAgent  string // agent listener address
	authStore  *auth.Store
	agentToken string
	userSessionToken string
	consoleURL string
)

func TestMain(m *testing.M) {
	code := runE2E(m)
	os.Exit(code)
}

func runE2E(m *testing.M) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // safety net for early returns; idempotent

	// ── Echo target (shared by both servers) ────────────────────────────
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: listen echo: %v\n", err)
		return 1
	}
	defer echoLn.Close()
	echoAddr = echoLn.Addr().String()
	go serveEcho(echoLn)

	// ── PostgreSQL + auth store ─────────────────────────────────────────
	pool, err := orcapostgres.OpenPool(ctx, orcapostgres.DBConfig{
		DatabaseURLTemplate: "postgres:tc:",
	}, orcapostgres.NewMigrator(migrations.FS, nil))
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: open pool: %v\n", err)
		return 1
	}
	authStore = auth.NewStore(pool)

	// Pre-create tokens used by auth tests.
	agentToken, err = auth.GenerateToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: generate agent token: %v\n", err)
		return 1
	}
	if err := authStore.CreateToken(ctx, agentToken, "agent", "e2e agent", nil); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: store agent token: %v\n", err)
		return 1
	}

	// Create a user and user session for connect client tests.
	pwHash, err := auth.HashPassword("e2epass")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: hash password: %v\n", err)
		return 1
	}
	testUser, err := authStore.CreateUser(ctx, "e2euser", pwHash, "admin", true, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: create user: %v\n", err)
		return 1
	}
	userSessionToken, err = auth.GenerateToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: generate user session: %v\n", err)
		return 1
	}
	if err := authStore.CreateUserSession(ctx, testUser.ID, userSessionToken, 24*time.Hour); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: create user session: %v\n", err)
		return 1
	}

	// ── No-auth server + agent ──────────────────────────────────────────
	noAuthSrv = server.NewServer(20 * time.Millisecond)
	noAuthHTTP := httptest.NewServer(noAuthSrv.HTTPHandler())
	defer noAuthHTTP.Close()

	noAuthAgentLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: listen no-auth agent: %v\n", err)
		return 1
	}
	defer noAuthAgentLn.Close()
	noAuthAgent = noAuthAgentLn.Addr().String()
	go noAuthSrv.ServeAgent(ctx, noAuthAgentLn)

	noAuthAg := &agent.Agent{
		ServerURL:  noAuthHTTP.URL,
		Target:     echoAddr,
		MaxBackoff: 50 * time.Millisecond,
	}
	go noAuthAg.Run(ctx)

	// ── Auth server + agent ─────────────────────────────────────────────
	authSrv = server.NewServer(20 * time.Millisecond)
	authSrv.SetAuthStore(authStore)
	authHTTP := httptest.NewServer(authSrv.HTTPHandler())
	defer authHTTP.Close()

	authAgentLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: listen auth agent: %v\n", err)
		return 1
	}
	defer authAgentLn.Close()
	authAgent = authAgentLn.Addr().String()
	go authSrv.ServeAgent(ctx, authAgentLn)

	authAg := &agent.Agent{
		ServerURL:  authHTTP.URL,
		Target:     echoAddr,
		Token:      agentToken,
		MaxBackoff: 50 * time.Millisecond,
	}
	go authAg.Run(ctx)

	// ── Console API (backed by auth server's registry) ──────────────────
	consoleRouter := consoleapi.NewRouter(authStore, authSrv.Reg)
	consoleTS := httptest.NewServer(consoleRouter)
	defer consoleTS.Close()
	consoleURL = consoleTS.URL

	// ── Wait for both agents to register ────────────────────────────────
	if err := waitAnySession(noAuthSrv.Reg, 10*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: no-auth agent: %v\n", err)
		return 1
	}
	if err := waitAnySession(authSrv.Reg, 10*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: auth agent: %v\n", err)
		return 1
	}

	// Cancel agent context BEFORE tearing down HTTP servers, so agents
	// stop their reconnect loops cleanly instead of spamming connection
	// refused errors while servers are shutting down.
	code := m.Run()
	cancel()
	return code
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// serveEcho accepts TCP connections and echoes bytes back.
func serveEcho(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			defer conn.Close()
			io.Copy(conn, conn)
		}(c)
	}
}

// waitAnySession polls until at least one session is registered.
func waitAnySession(reg *server.Registry, within time.Duration) error {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if len(reg.IDs()) > 0 {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("no session registered within %v", within)
}

// waitNewSession polls until a session other than notID appears.
func waitNewSession(reg *server.Registry, notID string, within time.Duration) (string, error) {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, id := range reg.IDs() {
			if id != notID {
				return id, nil
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return "", fmt.Errorf("no new session (other than %s) within %v", notID, within)
}

// dialAgent opens a raw TCP connection to the agent listener.
func dialAgent(addr string) (net.Conn, error) {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(30 * time.Second))
	return c, nil
}

// roundTrip writes want through conn and asserts a byte-exact echo.
func roundTrip(t *testing.T, c net.Conn, want []byte) {
	t.Helper()
	go func() { c.Write(want) }()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("echo not byte-exact")
	}
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i>>8)
	}
	return b
}

// ---------------------------------------------------------------------------
// No-auth full-cycle tests
// ---------------------------------------------------------------------------

// TestE2E_NoAuth_EchoByteExact: 1 MiB byte-exact echo through the full
// agent→tunnel→target→tunnel→agent path without authentication.
func TestE2E_NoAuth_EchoByteExact(t *testing.T) {
	if err := waitAnySession(noAuthSrv.Reg, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	c, err := dialAgent(noAuthAgent)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	defer c.Close()
	roundTrip(t, c, pattern(1<<20))
}

// TestE2E_NoAuth_ConcurrentEcho: two concurrent agent connections each
// echoing 256 KiB through the shared no-auth tunnel.
func TestE2E_NoAuth_ConcurrentEcho(t *testing.T) {
	if err := waitAnySession(noAuthSrv.Reg, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(fill int) {
			defer wg.Done()
			c, err := dialAgent(noAuthAgent)
			if err != nil {
				t.Errorf("dial agent: %v", err)
				return
			}
			defer c.Close()
			roundTrip(t, c, bytes.Repeat([]byte{byte(fill)}, 256<<10))
		}(i + 1)
	}
	wg.Wait()
}

// TestE2E_NoAuth_Reconnect: kills the active session and verifies the
// agent reconnects and new agent connections work again.
func TestE2E_NoAuth_Reconnect(t *testing.T) {
	if err := waitAnySession(noAuthSrv.Reg, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	// Verify tunnel works before killing.
	probe, err := dialAgent(noAuthAgent)
	if err != nil {
		t.Fatalf("dial probe: %v", err)
	}
	roundTrip(t, probe, []byte("before kill"))
	probe.Close()

	// Grab current session ID and kill it.
	ids := noAuthSrv.Reg.IDs()
	if len(ids) == 0 {
		t.Fatal("no session to kill")
	}
	id1 := ids[0]

	idle, err := dialAgent(noAuthAgent)
	if err != nil {
		t.Fatalf("dial idle: %v", err)
	}
	defer idle.Close()

	start := time.Now()
	noAuthSrv.Reg.Get(id1).Close()

	// Idle connection must close cleanly (no hang, no unbounded data).
	// During yamux shutdown a stray byte or two may leak through before
	// the server proxy's CloseWrite reaches us.  Drain and confirm the
	// connection actually closes (EOF / error) within the budget.
	idle.SetReadDeadline(start.Add(2 * time.Second))
	var drained int
	for {
		buf := make([]byte, 64)
		n, err := idle.Read(buf)
		drained += n
		if err != nil {
			break // EOF, timeout, or reset — connection closed cleanly
		}
		if drained > 64 {
			t.Fatalf("idle conn received %d bytes after session kill, proxy still alive", drained)
		}
	}
	if drained > 0 {
		t.Logf("drained %d shutdown bytes before close (harmless)", drained)
	}

	// Agent reconnects well under the 5 s budget.
	id2, err := waitNewSession(noAuthSrv.Reg, id1, 5*time.Second)
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("reconnect took %v, budget is 5s", elapsed)
	}
	t.Logf("reconnected as session %s in %v", id2, time.Since(start))

	// New agent connections work again.
	c, err := dialAgent(noAuthAgent)
	if err != nil {
		t.Fatalf("dial after reconnect: %v", err)
	}
	defer c.Close()
	roundTrip(t, c, pattern(64<<10))
}

// ---------------------------------------------------------------------------
// Auth full-cycle test
// ---------------------------------------------------------------------------

// TestE2E_Auth_FullCycle: end-to-end flow with authentication enabled —
// agent authenticates with a token, user connects via connect.Client with
// a user token, and the console API login + session listing work.
func TestE2E_Auth_FullCycle(t *testing.T) {
	ctx := context.Background()

	// Wait for auth agent session.
	if err := waitAnySession(authSrv.Reg, 5*time.Second); err != nil {
		t.Fatalf("auth agent session: %v", err)
	}

	// 1. Connect via connect.Client (handles agent token handshake).
	localLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen local: %v", err)
	}
	defer localLn.Close()

	clientCtx, clientCancel := context.WithCancel(ctx)
	defer clientCancel()

	client := connect.NewClient(authAgent, userSessionToken, "", "")
	go client.ServeListener(clientCtx, localLn)

	// 2. Dial the local client wrapper and echo a message.
	userConn, err := net.Dial("tcp", localLn.Addr().String())
	if err != nil {
		t.Fatalf("dial local: %v", err)
	}
	defer userConn.Close()

	testMsg := "hello end-to-end ssetunnel auth!\n"
	if _, err := fmt.Fprintf(userConn, "%s", testMsg); err != nil {
		t.Fatalf("write: %v", err)
	}
	reader := bufio.NewReader(userConn)
	echoResp, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if echoResp != testMsg {
		t.Fatalf("echo: want %q, got %q", testMsg, echoResp)
	}

	// 3. Console API: user-login with credentials.
	loginBody, _ := json.Marshal(map[string]string{
		"username": "e2euser",
		"password": "e2epass",
	})
	loginResp, err := http.Post(consoleURL+"/api/v1/user-login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login status: %d", loginResp.StatusCode)
	}

	var loginResult map[string]interface{}
	json.NewDecoder(loginResp.Body).Decode(&loginResult)
	consoleToken, _ := loginResult["token"].(string)

	// 4. Console API: list active sessions with bearer token.
	sessReq, _ := http.NewRequest("GET", consoleURL+"/api/v1/sessions", nil)
	sessReq.Header.Set("Authorization", "Bearer "+consoleToken)
	sessionsResp, err := (&http.Client{}).Do(sessReq)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	defer sessionsResp.Body.Close()
	if sessionsResp.StatusCode != http.StatusOK {
		t.Fatalf("sessions status: %d", sessionsResp.StatusCode)
	}
	var sessionList []consoleapi.SessionInfo
	json.NewDecoder(sessionsResp.Body).Decode(&sessionList)
	if len(sessionList) == 0 {
		t.Error("expected active session in console sessions list")
	}
}

// TestE2E_Auth_InvalidTokenRejected: a connect.Client with an invalid
// token must fail eagerly at startup, not silently accept connections
// that then fail on first use.
func TestE2E_Auth_InvalidTokenRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	client := connect.NewClient(authAgent, "invalid-token-should-be-rejected", "", "")
	err = client.ServeListener(ctx, ln)
	if err == nil {
		t.Fatal("expected ServeListener to fail with invalid token, got nil")
	}
	if !strings.Contains(err.Error(), "token validation failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("correctly rejected: %v", err)
}

// TestE2E_NoAuth_StdioRoundTrip: exercises the connect.Client.ServeRW
// path (used by --local - / SSH ProxyCommand) without authentication.
// Verifies bidirectional data flow: write data, read echo, then close.
func TestE2E_NoAuth_StdioRoundTrip(t *testing.T) {
	if err := waitAnySession(noAuthSrv.Reg, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdin: %v", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}

	client := connect.NewClient(noAuthAgent, "", "", "")

	done := make(chan error, 1)
	go func() {
		done <- client.ServeRW(ctx, stdinR, stdoutW)
	}()

	// Write test data (simulating SSH sending its protocol banner).
	testData := []byte("SSH-2.0-test\n")
	stdinW.Write(testData)

	// Read the echo back (simulating SSH receiving the server banner).
	got := make([]byte, len(testData))
	_, readErr := io.ReadFull(stdoutR, got)
	if readErr != nil {
		t.Fatalf("read echo: %v", readErr)
	}
	if !bytes.Equal(got, testData) {
		t.Fatalf("echo: want %q, got %q", testData, got)
	}

	// Now close stdin to signal we're done. ServeRW should return.
	stdinW.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServeRW: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeRW did not return after stdin close")
	}
}
