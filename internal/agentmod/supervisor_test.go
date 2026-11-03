//go:build !windows

package agentmod

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistryInstallRemove(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// Create a test executable.
	exe := filepath.Join(dir, "testmod")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	pkg := &Package{
		ModuleID:       "com.example.testmod",
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
		Executable:     exe,
		OpenAPIYAML:    []byte("openapi: 3.0.0\ninfo:\n  title: Test\n  version: 1.0.0\npaths:\n  /health:\n    get:\n      summary: Health\n"),
		PackageDir:     dir,
	}

	// Install.
	if err := reg.Install(pkg); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Verify installed.
	if got := reg.Package("com.example.testmod"); got == nil {
		t.Fatal("Package returned nil after install")
	}

	// Should be disabled by default.
	if reg.IsEnabled("com.example.testmod") {
		t.Error("module should be disabled after install")
	}

	// Enable.
	if err := reg.Enable("com.example.testmod"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !reg.IsEnabled("com.example.testmod") {
		t.Error("module should be enabled after Enable()")
	}

	// EnabledModules should include it.
	enabled := reg.EnabledModules()
	if len(enabled) != 1 || enabled[0] != "com.example.testmod" {
		t.Errorf("EnabledModules = %v, want [com.example.testmod]", enabled)
	}

	// Disable.
	if err := reg.Disable("com.example.testmod"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if reg.IsEnabled("com.example.testmod") {
		t.Error("module should be disabled after Disable()")
	}

	// Remove should succeed when disabled.
	if err := reg.Remove("com.example.testmod"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := reg.Package("com.example.testmod"); got != nil {
		t.Error("Package should return nil after remove")
	}
}

func TestRegistryRemoveEnabled(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	exe := filepath.Join(dir, "testmod")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	pkg := &Package{
		ModuleID:       "com.example.testmod",
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
		Executable:     exe,
		OpenAPIYAML:    []byte("openapi: 3.0.0\ninfo:\n  title: Test\n  version: 1.0.0\npaths: {}\n"),
		PackageDir:     dir,
	}

	if err := reg.Install(pkg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := reg.Enable("com.example.testmod"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	// Remove should fail when enabled.
	if err := reg.Remove("com.example.testmod"); err == nil {
		t.Error("expected error when removing enabled module")
	}
}

func TestRegistryPersistence(t *testing.T) {
	dir := t.TempDir()

	// Create first registry and install a module.
	reg1, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	exe := filepath.Join(dir, "testmod")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	pkg := &Package{
		ModuleID:       "com.example.testmod",
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
		Executable:     exe,
		OpenAPIYAML:    []byte("openapi: 3.0.0\ninfo:\n  title: Test\n  version: 1.0.0\npaths: {}\n"),
		PackageDir:     dir,
	}

	if err := reg1.Install(pkg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := reg1.Enable("com.example.testmod"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	// Create second registry (simulates restart).
	reg2, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// Desired state should persist.
	if !reg2.IsEnabled("com.example.testmod") {
		t.Error("desired state should persist across registry instances")
	}
}

func TestCalculateBackoff(t *testing.T) {
	// Backoff should increase with count.
	b1 := calculateBackoff(1)
	b5 := calculateBackoff(5)
	b10 := calculateBackoff(10)

	if b1 < 250*time.Millisecond || b1 > 750*time.Millisecond {
		t.Errorf("backoff(1) = %v, expected ~500ms", b1)
	}
	if b5 < 5*time.Second {
		t.Errorf("backoff(5) = %v, expected >5s", b5)
	}
	if b10 > 45*time.Second {
		t.Errorf("backoff(10) = %v, expected <=45s (capped at 30s + jitter)", b10)
	}
}

func TestSupervisorReconcile(t *testing.T) {
	dir := t.TempDir()

	reg, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	ipc, err := NewIPCServer()
	if err != nil {
		t.Fatalf("NewIPCServer: %v", err)
	}
	defer ipc.Close()

	// Start the IPC listener.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	endpoint := IPCEndpoint(dir)
	listener, err := ListenIPC(ctx, endpoint)
	if err != nil {
		t.Fatalf("ListenIPC: %v", err)
	}
	ipc.listener = listener

	// Create a simple test module that just sleeps.
	exe := filepath.Join(dir, "sleeper")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nsleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	pkg := &Package{
		ModuleID:       "com.example.sleeper",
		ServiceName:    "sleeper-service",
		ServiceVersion: "1.0.0",
		Executable:     exe,
		OpenAPIYAML:    []byte("openapi: 3.0.0\ninfo:\n  title: Sleeper\n  version: 1.0.0\npaths: {}\n"),
		PackageDir:     dir,
	}

	if err := reg.Install(pkg); err != nil {
		t.Fatalf("Install: %v", err)
	}

	sup := NewSupervisor(ctx, reg, ipc)
	defer sup.Stop()

	// Reconcile with no enabled modules should be a no-op.
	if err := sup.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Enable the module and reconcile.
	if err := reg.Enable("com.example.sleeper"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := sup.Reconcile(); err != nil {
		t.Fatalf("Reconcile after enable: %v", err)
	}

	// Give it a moment to start.
	time.Sleep(100 * time.Millisecond)

	// Process should be running.
	sup.mu.Lock()
	_, running := sup.processes["com.example.sleeper"]
	sup.mu.Unlock()
	if !running {
		t.Error("module should be running after reconcile")
	}

	// Disable and reconcile should stop it.
	if err := reg.Disable("com.example.sleeper"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if err := sup.Reconcile(); err != nil {
		t.Fatalf("Reconcile after disable: %v", err)
	}

	// Give it a moment to stop.
	time.Sleep(100 * time.Millisecond)

	sup.mu.Lock()
	_, running = sup.processes["com.example.sleeper"]
	sup.mu.Unlock()
	if running {
		t.Error("module should not be running after disable")
	}
}
