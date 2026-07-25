package server_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/server"
	"github.com/wseternal/ssetunnel/migrations"
	orcapostgres "github.com/visdomtech/orcacommon/postgres"
)

func TestEntryListenerHandshake(t *testing.T) {
	ctx := context.Background()

	dbcfg := orcapostgres.DBConfig{
		DatabaseURLTemplate: "postgres:tc:",
	}
	pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
	if err != nil {
		t.Fatalf("failed to open pool: %v", err)
	}

	store := auth.NewStore(pool)

	// Create a user and user session for the handshake test
	pwHash, err := auth.HashPassword("testpass")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	user, err := store.CreateUser(ctx, "handshake-user", pwHash, "user", true, true)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	sessionToken, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("failed to generate session token: %v", err)
	}
	err = store.CreateUserSession(ctx, user.ID, sessionToken, 24*time.Hour)
	if err != nil {
		t.Fatalf("failed to create user session: %v", err)
	}

	reg := server.NewRegistry()
	srv := server.NewServerWithRegistry(reg, 15*time.Second)
	srv.SetAuthStore(store)

	// Listen on ephemeral local port for entry connections
	entryListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on entry port: %v", err)
	}
	defer entryListener.Close()

	go srv.ServeEntry(ctx, entryListener)

	// 1. Connect with invalid token -> expect "ERR unauthorized\n"
	conn, err := net.Dial("tcp", entryListener.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial entry port: %v", err)
	}
	_, err = fmt.Fprintf(conn, "invalid-token\n")
	if err != nil {
		t.Fatalf("failed to write invalid token: %v", err)
	}

	r := bufio.NewReader(conn)
	resp, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("expected ERR response, got read error: %v", err)
	}
	if resp != "ERR unauthorized\n" {
		t.Errorf("expected \"ERR unauthorized\\n\", got %q", resp)
	}
	_ = conn.Close()

	// 2. Connect with valid user session -> expect OK\n
	conn2, err := net.Dial("tcp", entryListener.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial entry port: %v", err)
	}
	defer conn2.Close()

	_, err = fmt.Fprintf(conn2, "%s\n", sessionToken)
	if err != nil {
		t.Fatalf("failed to write valid session token: %v", err)
	}

	r2 := bufio.NewReader(conn2)
	resp2, err := r2.ReadString('\n')
	if err != nil || resp2 != "OK\n" {
		t.Fatalf("expected OK\\n, got resp=%q, err=%v", resp2, err)
	}
}
