package agentmod

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// TestIPCServer_AuthRegHealth tests the full IPC protocol flow:
// AUTH → OK → REG → HEALTH loop.
func TestIPCServer_AuthRegHealth(t *testing.T) {
	server, err := NewIPCServer()
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer server.Close()

	// Create a listener.
	endpoint := "/tmp/test-ipc-" + randomHex(4) + ".sock"
	defer os.Remove(endpoint)

	ln, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	server.SetListener(ln, endpoint)

	// Wait for accept loop to start.
	time.Sleep(50 * time.Millisecond)

	// Connect as a module process.
	conn, err := net.DialTimeout("unix", endpoint, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	cred := server.Credential()
	writer := bufio.NewWriter(conn)
	reader := bufio.NewReader(conn)

	// 1. Send AUTH.
	authMsg := fmt.Sprintf("AUTH %d\n%s", len(cred), cred)
	if _, err := writer.WriteString(authMsg); err != nil {
		t.Fatalf("write AUTH: %v", err)
	}
	writer.Flush()

	// 2. Read OK.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	okLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read OK: %v", err)
	}
	okLine = strings.TrimSuffix(okLine, "\n")
	if okLine != "OK" {
		t.Fatalf("expected OK, got %q", okLine)
	}

	// 3. Send REG.
	invEndpoint := endpoint + "-inv"
	regMsg := "REG test-mod TestService 1.0.0 abc123 " + invEndpoint + "\n"
	if _, err := writer.WriteString(regMsg); err != nil {
		t.Fatalf("write REG: %v", err)
	}
	writer.Flush()

	// Wait for registration to complete.
	time.Sleep(100 * time.Millisecond)

	// 4. Verify registration.
	reg := server.Lookup("test-mod")
	if reg == nil {
		t.Fatal("expected module to be registered")
	}
	if reg.ModuleID != "test-mod" {
		t.Errorf("module ID: got %s, want test-mod", reg.ModuleID)
	}
	if reg.ServiceName != "TestService" {
		t.Errorf("service name: got %s, want TestService", reg.ServiceName)
	}
	if reg.Version != "1.0.0" {
		t.Errorf("version: got %s, want 1.0.0", reg.Version)
	}
	if reg.Revision != "abc123" {
		t.Errorf("revision: got %s, want abc123", reg.Revision)
	}
	if reg.InvEndpoint != invEndpoint {
		t.Errorf("inv endpoint: got %s, want %s", reg.InvEndpoint, invEndpoint)
	}

	// 5. Send HEALTH update.
	healthMsg := "HEALTH degraded high latency\n"
	if _, err := writer.WriteString(healthMsg); err != nil {
		t.Fatalf("write HEALTH: %v", err)
	}
	writer.Flush()

	// Wait for health update.
	time.Sleep(50 * time.Millisecond)

	// 6. Verify health update.
	reg = server.Lookup("test-mod")
	if reg == nil {
		t.Fatal("module deregistered unexpectedly")
	}
	if reg.Health != "degraded" {
		t.Errorf("health: got %s, want degraded", reg.Health)
	}
}

// TestIPCServer_InvalidCredential verifies that invalid credentials are rejected.
func TestIPCServer_InvalidCredential(t *testing.T) {
	server, err := NewIPCServer()
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer server.Close()

	endpoint := "/tmp/test-badcred-" + randomHex(4) + ".sock"
	defer os.Remove(endpoint)

	ln, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	server.SetListener(ln, endpoint)
	time.Sleep(50 * time.Millisecond)

	conn, err := net.DialTimeout("unix", endpoint, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	writer := bufio.NewWriter(conn)
	reader := bufio.NewReader(conn)

	// Send AUTH with wrong credential.
	badCred := "wrong-credential"
	authMsg := fmt.Sprintf("AUTH %d\n%s", len(badCred), badCred)
	writer.WriteString(authMsg)
	writer.Flush()

	// Should receive ERR.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	errLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read ERR: %v", err)
	}
	errLine = strings.TrimSuffix(errLine, "\n")
	if errLine != "ERR" {
		t.Errorf("expected ERR, got %q", errLine)
	}
}

// TestIPCServer_DeregisterOnDisconnect verifies that modules are
// deregistered when their connection closes.
func TestIPCServer_DeregisterOnDisconnect(t *testing.T) {
	server, err := NewIPCServer()
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer server.Close()

	endpoint := "/tmp/test-discon-" + randomHex(4) + ".sock"
	defer os.Remove(endpoint)

	ln, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	server.SetListener(ln, endpoint)
	time.Sleep(50 * time.Millisecond)

	conn, err := net.DialTimeout("unix", endpoint, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	cred := server.Credential()
	writer := bufio.NewWriter(conn)
	reader := bufio.NewReader(conn)

	// AUTH.
	authMsg := fmt.Sprintf("AUTH %d\n%s", len(cred), cred)
	writer.WriteString(authMsg)
	writer.Flush()

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader.ReadString('\n') // OK

	// REG.
	writer.WriteString("REG disc-mod DiscService 1.0.0 rev1\n")
	writer.Flush()
	time.Sleep(100 * time.Millisecond)

	// Verify registered.
	if reg := server.Lookup("disc-mod"); reg == nil {
		t.Fatal("expected module to be registered")
	}

	// Close connection.
	conn.Close()

	// Wait for health loop to detect disconnect (60s timeout, but
	// the read error should be immediate since we closed the conn).
	time.Sleep(200 * time.Millisecond)

	// Verify deregistered.
	if reg := server.Lookup("disc-mod"); reg != nil {
		t.Error("expected module to be deregistered after disconnect")
	}
}

// randomHex returns a random hex string of the given byte length.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = strings.NewReader("").Read(b) // no-op
	// Use crypto/rand via the server's generateCredential.
	cred, _ := generateCredential()
	if len(cred) > n*2 {
		return cred[:n*2]
	}
	return cred
}
