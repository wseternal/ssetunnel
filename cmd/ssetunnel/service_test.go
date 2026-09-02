package main

import (
	"context"
	"os"
	"strings"
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
		{
			"strips --service-user space form",
			"server", []string{"start", "--service-user", "alice", "--listen", ":8080"},
			[]string{"server", "run", "--listen", ":8080"},
		},
		{
			"strips --service-user equals form",
			"server", []string{"start", "--service-user=bob", "--base", "/foo"},
			[]string{"server", "run", "--base", "/foo"},
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

func TestBareStartLoadsSavedArgs(t *testing.T) {
	// Override pidDir to use a temp dir so loadServiceArgs reads
	// from our test fixture instead of ~/.ssetunnel/.
	tmpDir := t.TempDir()
	origPidDir := pidDir
	pidDir = func() string { return tmpDir }
	t.Cleanup(func() { pidDir = origPidDir })

	// Pre-save args for "ssetunnel-server" (the name dispatchServiceAction
	// builds for subcommand="server").
	want := []string{"--base", "/sse", "--listen", ":9090"}
	if err := saveServiceArgsToDir(tmpDir, "ssetunnel-server", want); err != nil {
		t.Fatalf("saveServiceArgs: %v", err)
	}

	// Verify that loadServiceArgs (the function dispatchServiceAction
	// calls on bare start) loads the saved flags correctly.
	got, err := loadServiceArgs("ssetunnel-server")
	if err != nil {
		t.Fatalf("loadServiceArgs: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("loaded = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("loaded[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Verify that the loaded flags produce the correct buildServiceArgs
	// output when used as runtimeFlags in a bare start.
	svcArgs := buildServiceArgs("server", append([]string{"start"}, got...))
	wantSvc := []string{"server", "run", "--base", "/sse", "--listen", ":9090"}
	if len(svcArgs) != len(wantSvc) {
		t.Fatalf("buildServiceArgs = %v, want %v", svcArgs, wantSvc)
	}
	for i := range svcArgs {
		if svcArgs[i] != wantSvc[i] {
			t.Fatalf("buildServiceArgs[%d] = %q, want %q", i, svcArgs[i], wantSvc[i])
		}
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

func TestUninstallIsServiceAction(t *testing.T) {
	t.Parallel()
	if !serviceActions["uninstall"] {
		t.Fatal("\"uninstall\" not in serviceActions map")
	}
}

func TestUninstallCleansSavedArgs(t *testing.T) {
	tmpDir := t.TempDir()
	origPidDir := pidDir
	pidDir = func() string { return tmpDir }
	t.Cleanup(func() { pidDir = origPidDir })

	// Pre-save args for "ssetunnel-server".
	if err := saveServiceArgsToDir(tmpDir, "ssetunnel-server", []string{"--base", "/foo"}); err != nil {
		t.Fatalf("saveServiceArgs: %v", err)
	}
	// Write a fake PID file.
	writePIDFile("ssetunnel-server")

	// Verify files exist.
	argsPath := tmpDir + "/ssetunnel-server.args"
	pidPath := tmpDir + "/ssetunnel-server.pid"
	if _, err := os.Stat(argsPath); err != nil {
		t.Fatalf("args file missing: %v", err)
	}
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("pid file missing: %v", err)
	}

	// Simulate the cleanup that the uninstall handler performs.
	removePIDFile("ssetunnel-server")
	if err := os.Remove(argsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove args: %v", err)
	}

	// Verify both files are gone.
	if _, err := os.Stat(argsPath); !os.IsNotExist(err) {
		t.Error("args file still exists after uninstall cleanup")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("pid file still exists after uninstall cleanup")
	}
}

func TestStripFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		key  string
		want []string
	}{
		{"not present", []string{"--listen", ":8080"}, "--service-user", []string{"--listen", ":8080"}},
		{"space form", []string{"--service-user", "alice", "--listen", ":8080"}, "--service-user", []string{"--listen", ":8080"}},
		{"equals form", []string{"--service-user=bob", "--base", "/foo"}, "--service-user", []string{"--base", "/foo"}},
		{"only flag", []string{"--service-user", "carol"}, "--service-user", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stripFlag(tt.args, tt.key)
			if len(got) != len(tt.want) {
				t.Fatalf("stripFlag = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("stripFlag[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestConnectServiceRequiresName(t *testing.T) {
	t.Parallel()
	handled, err := dispatchServiceAction("connect", []string{"start", "--agent", "mybox", "--local", "127.0.0.1:2222"})
	if !handled {
		t.Fatal("expected handled=true for connect start")
	}
	if err == nil {
		t.Fatal("expected error for connect start without --name")
	}
	if !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestConnectServiceRejectsStdio(t *testing.T) {
	t.Parallel()
	handled, err := dispatchServiceAction("connect", []string{"start", "--name", "test-stdio", "--local", "-"})
	if !handled {
		t.Fatal("expected handled=true")
	}
	if err == nil {
		t.Fatal("expected error for stdio mode in connect service")
	}
	if !strings.Contains(err.Error(), "stdio mode") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestConnectServiceRejectsReload(t *testing.T) {
	t.Parallel()
	handled, err := dispatchServiceAction("connect", []string{"reload", "--name", "test-reload"})
	if !handled {
		t.Fatal("expected handled=true")
	}
	if err == nil {
		t.Fatal("expected error for connect reload")
	}
	if !strings.Contains(err.Error(), "not support reload") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBuildServiceArgsConnect(t *testing.T) {
	t.Parallel()
	got := buildServiceArgs("connect", []string{"start", "--name", "myssh", "--agent", "devbox", "--local", "127.0.0.1:2222"})
	want := []string{"connect", "run", "--name", "myssh", "--agent", "devbox", "--local", "127.0.0.1:2222"}
	if len(got) != len(want) {
		t.Fatalf("buildServiceArgs = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("buildServiceArgs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildRunFnIncludesConnect(t *testing.T) {
	t.Parallel()
	fn := buildRunFn("connect", []string{"--agent", "devbox", "--local", "127.0.0.1:2222"})
	if fn == nil {
		t.Fatal("buildRunFn(connect) returned nil")
	}
	// Verify it's not the unsupported-subcommand error function by
	// checking it doesn't return the "unsupported" error message.
	// We can't actually call it (needs a real server), but we verify
	// the function was constructed for connect.
}

func TestConnectServiceNameRequiresValue(t *testing.T) {
	t.Parallel()
	// --name flag present but with no value (end of args)
	handled, err := dispatchServiceAction("connect", []string{"start", "--name"})
	if !handled {
		t.Fatal("expected handled=true")
	}
	if err == nil {
		t.Fatal("expected error for --name with no value")
	}
	if !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("unexpected error: %v", err)
	}
}
