//go:build !windows

package agentmod

import (
	"context"
	"testing"
	"time"
)

func TestIPCServerBasic(t *testing.T) {
	srv, err := NewIPCServer()
	if err != nil {
		t.Fatalf("NewIPCServer: %v", err)
	}
	defer srv.Close()

	if srv.Credential() == "" {
		t.Error("credential should not be empty")
	}

	// Register a module.
	reg := &Registration{
		ModuleID:    "com.example.test",
		ServiceName: "test-service",
		Version:     "1.0.0",
		Revision:    "rev-1",
		Health:      "healthy",
	}
	if err := srv.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Lookup should find it.
	found := srv.Lookup("com.example.test")
	if found == nil {
		t.Fatal("Lookup returned nil")
	}
	if found.ModuleID != "com.example.test" {
		t.Errorf("ModuleID = %q, want com.example.test", found.ModuleID)
	}

	// Duplicate registration should fail.
	if err := srv.Register(reg); err == nil {
		t.Error("expected error for duplicate registration")
	}

	// Deregister.
	srv.Deregister("com.example.test")
	if found := srv.Lookup("com.example.test"); found != nil {
		t.Error("Lookup should return nil after deregister")
	}

	// Modules should be empty.
	if mods := srv.Modules(); len(mods) != 0 {
		t.Errorf("Modules() = %d entries, want 0", len(mods))
	}
}

func TestIPCServerClose(t *testing.T) {
	srv, err := NewIPCServer()
	if err != nil {
		t.Fatalf("NewIPCServer: %v", err)
	}

	// Register a module.
	reg := &Registration{ModuleID: "com.example.test"}
	if err := srv.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Close the server.
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Register after close should fail.
	if err := srv.Register(reg); err != ErrIPCClosed {
		t.Errorf("Register after close: got %v, want ErrIPCClosed", err)
	}

	// Close again should be a no-op.
	if err := srv.Close(); err != nil {
		t.Errorf("Close again: %v", err)
	}
}

func TestListenIPC(t *testing.T) {
	dir := t.TempDir()
	endpoint := IPCEndpoint(dir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	listener, err := ListenIPC(ctx, endpoint)
	if err != nil {
		t.Fatalf("ListenIPC: %v", err)
	}
	defer listener.Close()

	// Listen again on the same endpoint should succeed (we remove existing socket).
	listener2, err := ListenIPC(ctx, endpoint)
	if err != nil {
		t.Fatalf("ListenIPC second: %v", err)
	}
	listener2.Close()
}

func TestIPCEndpoint(t *testing.T) {
	dir := "/tmp/test-agent-data"
	endpoint := IPCEndpoint(dir)
	if endpoint != "/tmp/test-agent-data/module.sock" {
		t.Errorf("IPCEndpoint = %q, want /tmp/test-agent-data/module.sock", endpoint)
	}
}

func TestIPCClientServer(t *testing.T) {
	dir := t.TempDir()
	endpoint := IPCEndpoint(dir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start server.
	srv, err := NewIPCServer()
	if err != nil {
		t.Fatalf("NewIPCServer: %v", err)
	}
	defer srv.Close()

	listener, err := ListenIPC(ctx, endpoint)
	if err != nil {
		t.Fatalf("ListenIPC: %v", err)
	}
	srv.listener = listener

	// Accept connections in background.
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		// Read auth header.
		buf := make([]byte, 256)
		n, _ := conn.Read(buf)
		// Verify credential (simplified for test).
		conn.Write([]byte("OK\n"))
		// Read registration.
		n, _ = conn.Read(buf)
		_ = n
	}()

	// Connect client.
	client, err := DialIPC(ctx, endpoint, srv.Credential())
	if err != nil {
		t.Fatalf("DialIPC: %v", err)
	}
	defer client.Close()

	// Register.
	if err := client.Register("com.example.test", "test-service", "1.0.0", "rev-1"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Report health.
	if err := client.ReportHealth("healthy", "all good"); err != nil {
		t.Fatalf("ReportHealth: %v", err)
	}
}
