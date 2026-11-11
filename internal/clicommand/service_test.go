package clicommand

import (
	"context"
	"strings"
	"testing"
)

func TestBuildRunFnIncludesConnect(t *testing.T) {
	t.Parallel()
	fn := BuildRunFn("connect", []string{"--agent", "devbox", "--local", "127.0.0.1:2222"})
	if fn == nil {
		t.Fatal("BuildRunFn(connect) returned nil")
	}
	// Invoke with an already-cancelled context: the function should
	// propagate a parse or dial error, NOT the generic
	// "unsupported subcommand" sentinel.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := fn(ctx)
	if err != nil && strings.Contains(err.Error(), "unsupported subcommand") {
		t.Fatalf("BuildRunFn(connect) returned unsupported-subcommand error: %v", err)
	}
}

func TestBuildRunFnUnsupportedSubcommand(t *testing.T) {
	t.Parallel()
	fn := BuildRunFn("login", nil)
	err := fn(context.Background())
	if err == nil {
		t.Fatal("expected error for unsupported subcommand")
	}
	if !strings.Contains(err.Error(), "unsupported subcommand") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildRunFnIncludesAgent(t *testing.T) {
	t.Parallel()
	fn := BuildRunFn("agent", []string{"--server", "http://127.0.0.1:8080"})
	if fn == nil {
		t.Fatal("BuildRunFn(agent) returned nil")
	}
	// Invoke with a cancelled context: the function should propagate
	// a session/network error, NOT the generic "unsupported subcommand".
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := fn(ctx)
	if err != nil && strings.Contains(err.Error(), "unsupported subcommand") {
		t.Fatalf("BuildRunFn(agent) returned unsupported-subcommand error: %v", err)
	}
}

func TestBuildRunFnStripsServiceUser(t *testing.T) {
	t.Parallel()
	// --service-user is a service-installer flag, not a runtime flag.
	// BuildRunFn must strip it before passing args to RunAgent, so the
	// agent's flag parser does not reject it as an unknown flag.
	fn := BuildRunFn("agent", []string{"--service-user", "alice", "--server", "http://127.0.0.1:8080"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := fn(ctx)
	// The error should be a session/network error, not a flag parse error.
	if err != nil && strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("--service-user was not stripped before RunAgent: %v", err)
	}
}
