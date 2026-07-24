package connect_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
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

	userToken, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}
	err = store.CreateToken(ctx, userToken, "user", "test user", nil)
	if err != nil {
		t.Fatalf("failed to create user token: %v", err)
	}

	srv := server.NewServer(15 * time.Second)
	srv.SetAuthStore(store)

	entryListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen entry: %v", err)
	}
	defer entryListener.Close()

	go srv.ServeEntry(ctx, entryListener)

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

	client := connect.NewClient(entryListener.Addr().String(), userToken)

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
