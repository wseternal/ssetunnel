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

func TestRunServerParsesBareServiceArgs(t *testing.T) {
	t.Parallel()
	// The argv installed by DispatchServiceActionBare must parse every
	// flag: an occupied --listen port must surface "address already in
	// use". The legacy prefixed argv (["server","run",...]) made
	// flag.Parse stop at the first non-flag token and silently drop all
	// flags, so the daemon bound the default port instead.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// BuildRunArgs produces the installed argv ["run", …flags]; the
	// dispatcher strips the action verb before calling RunServer
	// (runtimeFlags := filteredArgs[1:]), so mirror that here.
	args := BuildRunArgs([]string{"start", "--listen", addr, "--console-listen=", "--metrics-dir=", "--disable-auth"})
	err = RunServer(ctx, args[1:])
	if err == nil {
		t.Fatal("expected error when address is already bound, got nil")
	}
	if !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("bare service argv did not parse --listen (flags dropped?): %v", err)
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
