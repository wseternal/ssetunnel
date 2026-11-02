package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEchoModule_FullPath tests the complete module lifecycle:
// 1. Build the echo module binary
// 2. Start an IPC server (Unix socket)
// 3. Spawn the echo module with IPC env vars
// 4. Wait for registration
// 5. Dial the invocation socket and send an HTTP request
// 6. Verify the echoed response
func TestEchoModule_FullPath(t *testing.T) {
	// Build the binary.
	tmpDir := t.TempDir()
	binary := filepath.Join(tmpDir, "echo")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	// Create IPC socket.
	endpoint := filepath.Join(tmpDir, "mod.sock")
	invEndpoint := endpoint + "-inv"

	ln, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Generate a random credential.
	cred := "test-cred-" + randomHex(4)

	// Accept one connection and handle IPC protocol.
	type moduleReg struct {
		moduleID    string
		invEndpoint string
	}
	regCh := make(chan moduleReg, 1)
	done := make(chan struct{})

	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)

		// Read AUTH message.
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		authLine, err := reader.ReadString('\n')
		if err != nil {
			t.Logf("read auth: %v", err)
			return
		}
		authLine = strings.TrimSuffix(authLine, "\n")
		if !strings.HasPrefix(authLine, "AUTH ") {
			t.Logf("expected AUTH, got %q", authLine)
			return
		}

		// Read credential bytes.
		var credLen int
		fmt.Sscanf(authLine, "AUTH %d", &credLen)
		credBuf := make([]byte, credLen)
		_, err = reader.Read(credBuf)
		if err != nil {
			t.Logf("read cred: %v", err)
			return
		}
		if string(credBuf) != cred {
			t.Logf("bad credential: got %q, want %q", string(credBuf), cred)
			return
		}

		// Send OK.
		writer.WriteString("OK\n")
		writer.Flush()

		// Read REG message.
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		regLine, err := reader.ReadString('\n')
		if err != nil {
			t.Logf("read reg: %v", err)
			return
		}
		regLine = strings.TrimSuffix(regLine, "\n")
		// REG format: REG <moduleID> <svcName> <ver> <rev> <invEndpoint>
		// Parse by splitting on first 5 spaces (revision may be empty).
		if !strings.HasPrefix(regLine, "REG ") {
			t.Logf("expected REG, got %q", regLine)
			return
		}
		rest := strings.TrimPrefix(regLine, "REG ")
		parts := strings.SplitN(rest, " ", 5)
		if len(parts) < 5 {
			t.Logf("REG too few fields: %q", regLine)
			return
		}
		// parts[0]=moduleID parts[1]=svcName parts[2]=ver parts[3]=rev parts[4]=invEndpoint
		regCh <- moduleReg{moduleID: parts[0], invEndpoint: parts[4]}

		// Read HEALTH messages (keep connection alive).
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "HEALTH") {
				// Health received, continue.
			}
		}
	}()

	// Spawn the echo module.
	modCmd := exec.Command(binary)
	modCmd.Env = append(os.Environ(),
		"SSETUNNEL_MODULE_IPC_ENDPOINT="+endpoint,
		"SSETUNNEL_MODULE_IPC_CREDENTIAL="+cred,
	)
	modCmd.Stdout = os.Stderr
	modCmd.Stderr = os.Stderr
	if err := modCmd.Start(); err != nil {
		t.Fatalf("start module: %v", err)
	}
	defer func() {
		modCmd.Process.Kill()
		modCmd.Wait()
	}()

	// Wait for registration.
	var reg moduleReg
	select {
	case reg = <-regCh:
		t.Logf("module registered: %s (inv endpoint: %s)", reg.moduleID, reg.invEndpoint)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for module registration")
	}

	if reg.moduleID != "com.example.echo" {
		t.Errorf("module ID: got %s, want com.example.echo", reg.moduleID)
	}
	if reg.invEndpoint == "" {
		t.Fatal("invocation endpoint is empty")
	}

	// Wait for the invocation socket to be ready.
	time.Sleep(200 * time.Millisecond)

	// Dial the invocation socket and send an HTTP POST request.
	invConn, err := net.DialTimeout("unix", invEndpoint, 5*time.Second)
	if err != nil {
		t.Fatalf("dial invocation socket: %v", err)
	}
	defer invConn.Close()

	// Send HTTP POST request.
	body := `{"message": "hello from test"}`
	reqLine := fmt.Sprintf("POST /echo HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	if _, err := invConn.Write([]byte(reqLine)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	// Read the HTTP response.
	invConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(invConn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	// Parse the echo response.
	var echo echoResponse
	if err := json.NewDecoder(resp.Body).Decode(&echo); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if echo.Method != "POST" {
		t.Errorf("method: got %s, want POST", echo.Method)
	}
	if echo.Path != "/echo" {
		t.Errorf("path: got %s, want /echo", echo.Path)
	}
	if echo.Body != body {
		t.Errorf("body: got %s, want %s", echo.Body, body)
	}
	if ct := echo.Headers["Content-Type"]; len(ct) == 0 || ct[0] != "application/json" {
		t.Errorf("content-type header: got %v, want [application/json]", ct)
	}

	t.Logf("echo response: method=%s path=%s body=%s", echo.Method, echo.Path, echo.Body)
}

func randomHex(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = "0123456789abcdef"[time.Now().UnixNano()%16]
		time.Sleep(time.Nanosecond)
	}
	return string(b)
}
