package command

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
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

			err = RunServer(ctx, args)
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
			if got := IsEmbeddedPostgres(tt.dbURL); got != tt.want {
				t.Errorf("IsEmbeddedPostgres(%q) = %v, want %v", tt.dbURL, got, tt.want)
			}
		})
	}
}
