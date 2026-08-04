package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/server"
)

func TestDispatchServiceAction_NonActionVerb(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
	}{
		{"empty args", nil},
		{"flag only", []string{"--disable-auth"}},
		{"unknown verb", []string{"foobar"}},
		{"flag with value", []string{"--listen", ":9090"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handled, err := dispatchServiceAction("server", tt.args)
			if handled {
				t.Fatalf("dispatchServiceAction(%q) = handled=true, want false", tt.args)
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestExtractFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		args      []string
		key       string
		wantValue string
		wantRest  []string
	}{
		{"not present", []string{"run", "--listen", ":8080"}, "--service-user", "", []string{"run", "--listen", ":8080"}},
		{"space form", []string{"start", "--service-user", "alice", "--listen", ":8080"}, "--service-user", "alice", []string{"start", "--listen", ":8080"}},
		{"equals form", []string{"start", "--service-user=bob", "--listen", ":8080"}, "--service-user", "bob", []string{"start", "--listen", ":8080"}},
		{"only flag", []string{"run", "--service-user", "carol"}, "--service-user", "carol", []string{"run"}},
		{"flag at end no value", []string{"run", "--service-user"}, "--service-user", "", []string{"run", "--service-user"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotValue, gotRest := extractFlag(tt.args, tt.key)
			if gotValue != tt.wantValue {
				t.Errorf("value = %q, want %q", gotValue, tt.wantValue)
			}
			if len(gotRest) != len(tt.wantRest) {
				t.Fatalf("remaining = %v, want %v", gotRest, tt.wantRest)
			}
			for i := range gotRest {
				if gotRest[i] != tt.wantRest[i] {
					t.Fatalf("remaining[%d] = %q, want %q", i, gotRest[i], tt.wantRest[i])
				}
			}
		})
	}
}

func TestBuildServiceArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		subcommand string
		args       []string
		want       []string
	}{
		{
			"run with flags",
			"server", []string{"run", "--listen", ":8080", "--disable-auth"},
			[]string{"server", "run", "--listen", ":8080", "--disable-auth"},
		},
		{
			"start with flags",
			"agent", []string{"start", "--server", "http://x", "--id", "dev"},
			[]string{"agent", "run", "--server", "http://x", "--id", "dev"},
		},
		{
			"bare action",
			"server", []string{"run"},
			[]string{"server", "run"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildServiceArgs(tt.subcommand, tt.args)
			if len(got) != len(tt.want) {
				t.Fatalf("buildServiceArgs = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("buildServiceArgs[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestServiceProgram_StartStop(t *testing.T) {
	t.Parallel()
	var started atomic.Bool
	var stopped atomic.Bool

	prg := &serviceProgram{
		name: "test-svc",
		runFn: func(ctx context.Context) error {
			started.Store(true)
			<-ctx.Done()
			stopped.Store(true)
			return nil
		},
	}

	if err := prg.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Give the goroutine time to start.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !started.Load() {
		time.Sleep(time.Millisecond)
	}
	if !started.Load() {
		t.Fatal("runFn never started")
	}

	if err := prg.Stop(nil); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !stopped.Load() {
		t.Fatal("runFn did not observe ctx cancellation")
	}
}

func TestServiceProgram_StopTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 30s timeout test in short mode")
	}
	t.Parallel()
	prg := &serviceProgram{
		name: "test-hang",
		runFn: func(ctx context.Context) error {
			// Ignore cancellation — simulates a hung worker.
			time.Sleep(2 * time.Minute)
			return nil
		},
	}

	if err := prg.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := time.Now()
	if err := prg.Stop(nil); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	elapsed := time.Since(start)
	// Stop should return after ~30s timeout, not wait 2 minutes.
	// Use 35s ceiling to allow for scheduling jitter.
	if elapsed > 35*time.Second {
		t.Fatalf("Stop took %v, expected ~30s timeout", elapsed)
	}
}

func TestSaveLoadServiceArgs(t *testing.T) {
	t.Parallel()
	// Use a temp dir to avoid touching the real ~/.ssetunnel/.
	tmpDir := t.TempDir()
	name := "test-svc-" + t.Name()

	// Load from non-existent file returns nil.
	args, err := loadServiceArgsFromDir(tmpDir, name)
	if err != nil {
		t.Fatalf("loadServiceArgs (missing): %v", err)
	}
	if args != nil {
		t.Fatalf("loadServiceArgs (missing) = %v, want nil", args)
	}

	// Save and load roundtrip.
	want := []string{"--base", "/sse", "--db-url", "postgres:embedded:?datapath=/tmp/data"}
	if err := saveServiceArgsToDir(tmpDir, name, want); err != nil {
		t.Fatalf("saveServiceArgs: %v", err)
	}
	got, err := loadServiceArgsFromDir(tmpDir, name)
	if err != nil {
		t.Fatalf("loadServiceArgs: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("roundtrip = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("roundtrip[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestServiceUserPersistedInSavedArgs(t *testing.T) {
	t.Parallel()
	// Verify that --service-user survives save/load roundtrip so a
	// subsequent bare "start" can reconstruct the service identity.
	tmpDir := t.TempDir()
	name := "test-svc-user"

	saved := []string{"--service-user", "svcuser", "--base", "/foo"}
	if err := saveServiceArgsToDir(tmpDir, name, saved); err != nil {
		t.Fatalf("saveServiceArgs: %v", err)
	}
	got, err := loadServiceArgsFromDir(tmpDir, name)
	if err != nil {
		t.Fatalf("loadServiceArgs: %v", err)
	}

	// extractFlag should recover --service-user from the loaded args.
	svcUser, remaining := extractFlag(got, "--service-user")
	if svcUser != "svcuser" {
		t.Errorf("extracted --service-user = %q, want %q", svcUser, "svcuser")
	}
	// Remaining should be just --base /foo.
	if len(remaining) != 2 || remaining[0] != "--base" || remaining[1] != "/foo" {
		t.Errorf("remaining after extract = %v, want [--base /foo]", remaining)
	}
}

func TestRegistry_CloseAll(t *testing.T) {
	t.Parallel()
	reg := server.NewRegistry()
	sessions := make([]*server.Session, 5)
	for i := range sessions {
		s := server.NewSession("test-" + string(rune('a'+i)))
		sessions[i] = s
		reg.Replace(s)
	}

	reg.CloseAll()

	// Verify all sessions are closed by checking Done channels.
	for i, s := range sessions {
		select {
		case <-s.Done():
			// OK — session is closed.
		default:
			t.Fatalf("session %d not closed after CloseAll", i)
		}
	}
}
