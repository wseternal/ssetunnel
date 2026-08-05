package main

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/auth"
)

func TestRunServer_AddressAlreadyBound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		bindFlag string // "listen" or "console-listen"
	}{
		{"listen address bound", "listen"},
		{"console-listen address bound", "console-listen"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Occupy a port
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("setup listen: %v", err)
			}
			defer ln.Close()
			addr := ln.Addr().String()

			// 30 s: the console-listen subtest provisions a TestContainer,
			// runs migrations, and seeds an admin user — all on this context.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			args := []string{"--disable-auth", "--metrics-dir="}
			switch tt.bindFlag {
			case "listen":
				args = append(args, "--listen", addr)
			case "console-listen":
				// console-listen only opens when auth is enabled; use
				// --db-url with testcontainer to enable the console path.
				args = []string{
					"--listen", "127.0.0.1:0",
					"--console-listen", addr,
					"--metrics-dir=",
				}
			}

			err = runServer(ctx, args)
			if err == nil {
				t.Fatal("expected error when address is already bound, got nil")
			}
			if !strings.Contains(err.Error(), "address already in use") &&
				!strings.Contains(err.Error(), "bind") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestClampAgentFlags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		batchSize       int
		concurrency     int
		wantBatchSize   int
		wantConcurrency int
	}{
		{"defaults pass through", 16384, 1, 16384, 1},
		{"tuned values pass through", 65536, 4, 65536, 4},
		{"batch below min clamps up", 512, 1, 1024, 1},
		{"batch above max clamps down", 2 << 20, 1, 1 << 20, 1},
		{"concurrency below min clamps up", 16384, 0, 16384, 1},
		{"concurrency above max clamps down", 16384, 8, 16384, 4},
		{"both clamp", 1, 99, 1024, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotBatch, gotConc := clampAgentFlags(tt.batchSize, tt.concurrency)
			if gotBatch != tt.wantBatchSize || gotConc != tt.wantConcurrency {
				t.Fatalf("clampAgentFlags(%d, %d) = (%d, %d), want (%d, %d)",
					tt.batchSize, tt.concurrency, gotBatch, gotConc, tt.wantBatchSize, tt.wantConcurrency)
			}
		})
	}
}

func TestDeriveTunnelURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{"default ports", "http://127.0.0.1:8081", "http://127.0.0.1:8080"},
		{"https default ports", "https://tunnel.example.com:8081", "https://tunnel.example.com:8080"},
		{"non-default port unchanged", "http://host:9081", "http://host:9081"},
		{"no port unchanged", "http://host", "http://host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := deriveTunnelURL(tt.input)
			if got != tt.expect {
				t.Errorf("deriveTunnelURL(%q) = %q, want %q", tt.input, got, tt.expect)
			}
		})
	}
}

func TestIsEmbeddedPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		dbURL string
		want  bool
	}{
		{"postgres:tc:", true},
		{"postgres:embedded:", true},
		{"postgres://user:pass@host:5432/db", false},
		{"postgresql://host/db", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.dbURL, func(t *testing.T) {
			t.Parallel()
			if got := isEmbeddedPostgres(tt.dbURL); got != tt.want {
				t.Errorf("isEmbeddedPostgres(%q) = %v, want %v", tt.dbURL, got, tt.want)
			}
		})
	}
}

// testHomeDir sets up a temporary HOME for session file tests.
func testHomeDir(t *testing.T) func() {
	t.Helper()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	return func() { os.Setenv("HOME", origHome) }
}

func TestResolveServerURL_NoFlag_NoSession(t *testing.T) {
	cleanup := testHomeDir(t)
	defer cleanup()

	_, _, err := resolveServerURL("", "test")
	if err == nil {
		t.Fatal("expected error when no flag and no session, got nil")
	}
	if !strings.Contains(err.Error(), "--server is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveServerURL_NoFlag_WithSession(t *testing.T) {
	cleanup := testHomeDir(t)
	defer cleanup()

	if err := auth.SaveSession("http://saved:8080", "tok1", "user1", "admin"); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	url, token, err := resolveServerURL("", "test")
	if err != nil {
		t.Fatalf("resolveServerURL: %v", err)
	}
	if url != "http://saved:8080" {
		t.Errorf("url = %q, want %q", url, "http://saved:8080")
	}
	if token != "tok1" {
		t.Errorf("token = %q, want %q", token, "tok1")
	}
}

func TestResolveServerURL_Flag_WithSession(t *testing.T) {
	cleanup := testHomeDir(t)
	defer cleanup()

	if err := auth.SaveSession("http://flag:8080", "tok2", "user2", "admin"); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	url, token, err := resolveServerURL("http://flag:8080", "test")
	if err != nil {
		t.Fatalf("resolveServerURL: %v", err)
	}
	if url != "http://flag:8080" {
		t.Errorf("url = %q, want %q", url, "http://flag:8080")
	}
	if token != "tok2" {
		t.Errorf("token = %q, want %q", token, "tok2")
	}
}

func TestResolveServerURL_Flag_NoSession(t *testing.T) {
	cleanup := testHomeDir(t)
	defer cleanup()

	url, token, err := resolveServerURL("http://explicit:8080", "test")
	if err != nil {
		t.Fatalf("resolveServerURL: %v", err)
	}
	if url != "http://explicit:8080" {
		t.Errorf("url = %q, want %q", url, "http://explicit:8080")
	}
	if token != "" {
		t.Errorf("token = %q, want empty", token)
	}
}

func TestResolveServerURL_TrailingSlash(t *testing.T) {
	cleanup := testHomeDir(t)
	defer cleanup()

	if err := auth.SaveSession("http://host:8080", "tok", "user", "admin"); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	url, token, err := resolveServerURL("http://host:8080/", "test")
	if err != nil {
		t.Fatalf("resolveServerURL: %v", err)
	}
	if url != "http://host:8080" {
		t.Errorf("url = %q, want %q (trailing slash not trimmed)", url, "http://host:8080")
	}
	if token != "tok" {
		t.Errorf("token = %q, want %q", token, "tok")
	}
}
