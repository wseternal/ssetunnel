package connect_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/connect"
	"github.com/wseternal/ssetunnel/internal/mux"
	"github.com/wseternal/ssetunnel/internal/server"
	"github.com/wseternal/ssetunnel/migrations"
	orcapostgres "github.com/visdomtech/orcacommon/postgres"
)

func TestConnectClient_LocalPortMode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbcfg := orcapostgres.DBConfig{
		DatabaseURLTemplate: "postgres:tc:",
	}
	pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
	if err != nil {
		t.Fatalf("failed to open pool: %v", err)
	}

	store := auth.NewStore(pool)

	// Create a user and user session for the connect client.
	pwHash, err := auth.HashPassword("connecttest")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	testUser, err := store.CreateUser(ctx, "connectuser", pwHash, "user", true, true)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	userToken, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}
	err = store.CreateUserSession(ctx, testUser.ID, userToken, 24*time.Hour)
	if err != nil {
		t.Fatalf("failed to create user session: %v", err)
	}

	srv := server.NewServer(15 * time.Second)
	srv.SetAuthStore(store)
	httpSrv := httptest.NewServer(srv.HTTPHandler())
	defer httpSrv.Close()

	// Create echo target listener
	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen echo target: %v", err)
	}
	defer echoListener.Close()

	go func() {
		for {
			c, err := echoListener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	// Connect agent and server over real TCP loopback
	pipeListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen pipe listener: %v", err)
	}
	defer pipeListener.Close()

	agentPipeChan := make(chan net.Conn, 1)
	go func() {
		c, err := pipeListener.Accept()
		if err == nil {
			agentPipeChan <- c
		}
	}()

	srvPipe, err := net.Dial("tcp", pipeListener.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial pipe listener: %v", err)
	}
	defer srvPipe.Close()

	agentPipe := <-agentPipeChan
	defer agentPipe.Close()

	// Create agent side yamux client
	agentYamux, err := mux.Client(agentPipe)
	if err != nil {
		t.Fatalf("failed to create agent yamux client: %v", err)
	}

	// Accept streams on agent yamux and proxy to echo target
	go func() {
		for {
			stream, err := agentYamux.AcceptStream()
			if err != nil {
				return
			}
			go func(st net.Conn) {
				defer st.Close()
				targetConn, err := net.Dial("tcp", echoListener.Addr().String())
				if err != nil {
					return
				}
				defer targetConn.Close()
				go func() { _, _ = io.Copy(targetConn, st) }()
				_, _ = io.Copy(st, targetConn)
			}(stream)
		}
	}()

	// Attach server yamux session directly
	srv.AttachConn(srvPipe)

	// Start connect client wrapper on ephemeral local port
	localListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen local client port: %v", err)
	}
	defer localListener.Close()

	client := connect.NewClient(httpSrv.URL, userToken, "", "", "")

	go func() {
		_ = client.ServeListener(ctx, localListener)
	}()

	// Connect user client to local wrapper port
	userConn, err := net.Dial("tcp", localListener.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial local client wrapper: %v", err)
	}
	defer userConn.Close()

	// Send data over local wrapper
	testData := "hello ssetunnel cycle 3!\n"
	_, err = fmt.Fprintf(userConn, "%s", testData)
	if err != nil {
		t.Fatalf("failed to write data: %v", err)
	}

	reader := bufio.NewReader(userConn)
	echoResp, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read echo response: %v", err)
	}

	if echoResp != testData {
		t.Errorf("expected echo %q, got %q", testData, echoResp)
	}
}

// TestServeRW_ServerClosesReturns verifies that ServeRW returns promptly
// when the remote target closes its connection, even if the local reader
// (stdin) is still open. This is the SSH ProxyCommand deadlock scenario:
// sshd sends the keyboard-interactive prompt and closes, but the SSH client
// is waiting for the prompt and won't close stdin. ServeRW must detect the
// server-side EOF and exit so the SSH client sees EOF on the pipe.
func TestServeRW_ServerClosesReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Target: sends a greeting then closes (simulating sshd sending a
	// keyboard-interactive prompt and then closing).
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetLn.Close()
	go func() {
		for {
			c, err := targetLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				conn.Write([]byte("Password: "))
				// Target closes immediately after sending,
				// simulating sshd behaviour on macOS without PAM.
			}(c)
		}
	}()

	// Server (no auth, direct yamux pipe)
	srv := server.NewServer(15 * time.Second)
	srv.SetAuthStore(nil) // register /connect routes
	httpSrv := httptest.NewServer(srv.HTTPHandler())
	defer httpSrv.Close()

	// Agent side: pipe-based yamux
	pipeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen pipe: %v", err)
	}
	defer pipeLn.Close()

	agentConnCh := make(chan net.Conn, 1)
	go func() {
		c, err := pipeLn.Accept()
		if err == nil {
			agentConnCh <- c
		}
	}()

	srvConn, err := net.Dial("tcp", pipeLn.Addr().String())
	if err != nil {
		t.Fatalf("dial pipe: %v", err)
	}
	defer srvConn.Close()

	agentConn := <-agentConnCh
	defer agentConn.Close()

	agentYamux, err := mux.Client(agentConn)
	if err != nil {
		t.Fatalf("agent yamux: %v", err)
	}

	// Agent: accept streams and proxy to target
	go func() {
		for {
			stream, err := agentYamux.AcceptStream()
			if err != nil {
				return
			}
			go func(st net.Conn) {
				tgt, err := net.Dial("tcp", targetLn.Addr().String())
				if err != nil {
					st.Close()
					return
				}
				go func() { io.Copy(tgt, st); tgt.Close() }()
				go func() { io.Copy(st, tgt); st.Close() }()
			}(stream)
		}
	}()

	srv.AttachConn(srvConn)

	// Wait for session
	time.Sleep(200 * time.Millisecond)

	// Connect with ServeRW using pipes (simulating SSH ProxyCommand).
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdin: %v", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}

	client := connect.NewClient(httpSrv.URL, "", "", "", "")
	done := make(chan error, 1)
	go func() {
		done <- client.ServeRW(ctx, stdinR, stdoutW)
	}()

	// Read the greeting from the target (simulating SSH client reading
	// the keyboard-interactive prompt).
	buf := make([]byte, 1024)
	stdoutR.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := stdoutR.Read(buf)
	if err != nil {
		t.Fatalf("read greeting: %v", err)
	}
	if !bytes.Contains(buf[:n], []byte("Password: ")) {
		t.Fatalf("unexpected greeting: %q", buf[:n])
	}

	// Now stdin is still open (SSH client waiting for user input).
	// ServeRW should return because the server side closed.
	select {
	case err := <-done:
		// err may be non-nil (connection reset from Close) — that's fine.
		_ = err
	case <-time.After(5 * time.Second):
		stdinW.Close()
		t.Fatal("ServeRW deadlocked: did not return after server-side close")
	}

	// Cleanup
	stdinW.Close()
	stdoutR.Close()
}

// TestServeRW_HandshakeFailureWritesToWriter verifies that when the
// handshake fails, ServeRW writes a user-friendly error message to w
// (stdout in ProxyCommand mode) so the SSH client can display it to
// the user instead of only showing "Connection closed by UNKNOWN port 65535".
func TestServeRW_HandshakeFailureWritesToWriter(t *testing.T) {
	ctx := context.Background()

	// Use a closed port so the HTTP dial fails immediately.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // close immediately so dial fails

	var stdout bytes.Buffer
	client := connect.NewClient("http://"+addr, "bad-token", "", "", "")

	err = client.ServeRW(ctx, strings.NewReader(""), &stdout)
	if err == nil {
		t.Fatal("expected error from ServeRW, got nil")
	}

	output := stdout.String()
	if !strings.Contains(output, "ssetunnel:") {
		t.Errorf("expected friendly error in stdout, got: %q", output)
	}
	if !strings.Contains(output, "dial server") {
		t.Errorf("expected dial error detail in stdout, got: %q", output)
	}
}
