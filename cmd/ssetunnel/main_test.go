package main

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
		bindFlag string // "listen", "agent", or "console-listen"
	}{
		{"listen address bound", "listen"},
		{"agent address bound", "agent"},
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

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			args := []string{"--disable-auth"}
			switch tt.bindFlag {
			case "listen":
				args = append(args, "--listen", addr, "--agent", "127.0.0.1:0")
			case "agent":
				args = append(args, "--listen", "127.0.0.1:0", "--agent", addr)
			case "console-listen":
				// console-listen only opens when auth is enabled; use
				// --db-url with testcontainer to enable the console path.
				args = []string{
					"--listen", "127.0.0.1:0",
					"--agent", "127.0.0.1:0",
					"--console-listen", addr,
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
