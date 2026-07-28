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

func TestE2E_Cycle3_FullFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Provision TestContainer PostgreSQL database
	dbcfg := orcapostgres.DBConfig{
		DatabaseURLTemplate: "postgres:tc:",
	}
	pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
	if err != nil {
		t.Fatalf("failed to open pool: %v", err)
	}

	store := auth.NewStore(pool)
	totpSecret := "JBSWY3DPEHPK3PXP" // test TOTP secret

	// 2. Start Tunnel Server
	srv := server.NewServer(15 * time.Second)
	srv.SetAuthStore(store)

	httpServer := httptest.NewServer(srv.HTTPHandler())
	defer httpServer.Close()

	entryListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen entry: %v", err)
	}
	defer entryListener.Close()

	go srv.ServeEntry(ctx, entryListener)

	// 3. Start Console API Router
	consoleRouter := consoleapi.NewRouter(store, srv.Reg, totpSecret)
	consoleServer := httptest.NewServer(consoleRouter)
	defer consoleServer.Close()

	// 4. Enroll Agent via Token
	agentToken, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("failed to generate agent token: %v", err)
	}
	err = store.CreateToken(ctx, agentToken, "agent", "e2e agent", nil)
	if err != nil {
		t.Fatalf("failed to store agent token: %v", err)
	}

	// 5. Start Echo Target Service
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

	// 6. Start Agent with Token
	ag := &agent.Agent{
		ServerURL: httpServer.URL,
		Target:    echoListener.Addr().String(),
		Token:     agentToken,
	}

	go func() {
		_ = ag.Run(ctx)
	}()

	// Wait for agent session to register in srv.Reg
	var activeSess *server.Session
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		srv.Reg.Range(func(s *server.Session) bool {
			activeSess = s
			return false
		})
		if activeSess != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if activeSess == nil {
		t.Fatalf("timed out waiting for agent session to register")
	}

	// 7. Generate User Token for Entry Client Handshake
	userToken, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("failed to generate user token: %v", err)
	}
	err = store.CreateToken(ctx, userToken, "user", "e2e user", nil)
	if err != nil {
		t.Fatalf("failed to store user token: %v", err)
	}

	// 8. Start Connect Client Wrapper on ephemeral local port
	localListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen local client port: %v", err)
	}
	defer localListener.Close()

	client := connect.NewClient(entryListener.Addr().String(), userToken)
	go func() {
		_ = client.ServeListener(ctx, localListener)
	}()

	// 9. Dial Local Client Wrapper and send test payload
	userConn, err := net.Dial("tcp", localListener.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial local client wrapper: %v", err)
	}
	defer userConn.Close()

	testMsg := "hello end-to-end ssetunnel cycle 3!\n"
	if _, err := fmt.Fprintf(userConn, "%s", testMsg); err != nil {
		t.Fatalf("failed to write test message: %v", err)
	}

	reader := bufio.NewReader(userConn)
	echoResp, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read echo response: %v", err)
	}

	if echoResp != testMsg {
		t.Errorf("expected echo %q, got %q", testMsg, echoResp)
	}

	// 10. Verify Console API Admin TOTP Login & Active Sessions Stats
	totpCode, err := auth.GenerateTOTPCode(totpSecret)
	if err != nil {
		t.Fatalf("failed to generate TOTP code: %v", err)
	}

	loginBody, _ := json.Marshal(map[string]string{"totp_code": totpCode})
	loginResp, err := http.Post(consoleServer.URL+"/api/v1/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 login status, got %d", loginResp.StatusCode)
	}

	sessCookies := loginResp.Cookies()
	sessReq, _ := http.NewRequest("GET", consoleServer.URL+"/api/v1/sessions", nil)
	for _, c := range sessCookies {
		sessReq.AddCookie(c)
	}

	clientHTTP := &http.Client{}
	sessionsResp, err := clientHTTP.Do(sessReq)
	if err != nil {
		t.Fatalf("failed to fetch sessions: %v", err)
	}
	if sessionsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 sessions status, got %d", sessionsResp.StatusCode)
	}

	var sessionList []consoleapi.SessionInfo
	_ = json.NewDecoder(sessionsResp.Body).Decode(&sessionList)
	if len(sessionList) == 0 {
		t.Errorf("expected active session in console sessions list")
	}
}
