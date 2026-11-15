package clicommand

import (
	"context"
	"strings"
	"testing"
)

func TestBuildRunFnIncludesConnect(t *testing.T) {
	t.Parallel()
	// An unknown flag must reach RunConnect's flag parser: a parse error
	// proves dispatch went to the connect workflow, not the generic
	// "unsupported subcommand" sentinel.
	fn := BuildRunFn("connect", []string{"--bogus-flag"})
	if fn == nil {
		t.Fatal("BuildRunFn(connect) returned nil")
	}
	err := fn(context.Background())
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("expected connect flag-parse error, got: %v", err)
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
	// An unknown flag must reach RunAgent's flag parser (see connect test).
	fn := BuildRunFn("agent", []string{"--bogus-flag"})
	if fn == nil {
		t.Fatal("BuildRunFn(agent) returned nil")
	}
	err := fn(context.Background())
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("expected agent flag-parse error, got: %v", err)
	}
}

func TestBuildRunFnStripsServiceUser(t *testing.T) {
	t.Parallel()
	// --service-user is a service-installer flag, not a runtime flag.
	// BuildRunFn must strip it before passing args to RunAgent: the parse
	// error must name the bogus flag, never --service-user.
	fn := BuildRunFn("agent", []string{"--service-user", "alice", "--bogus-flag"})
	err := fn(context.Background())
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("expected flag-parse error, got: %v", err)
	}
	if strings.Contains(err.Error(), "service-user") {
		t.Fatalf("--service-user was not stripped before RunAgent: %v", err)
	}
}
