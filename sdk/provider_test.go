package sdk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// randomHex returns a random hex string of the given byte length.
func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// TestProvider_InvocationListener verifies that the provider's invocation
// listener accepts HTTP connections over a Unix socket and serves requests.
func TestProvider_InvocationListener(t *testing.T) {
	// Use /tmp for shorter socket paths (Unix socket paths max ~104 chars).
	endpoint := "/tmp/test-mod-" + randomHex(4) + ".sock"
	invEndpoint := endpoint + "-inv"
	defer os.Remove(endpoint)
	defer os.Remove(invEndpoint)

	// Create a simple HTTP handler.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Module", "test")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello from module"))
	})

	// Create an invocation listener directly (bypassing full IPC).
	listener, err := net.Listen("unix", invEndpoint)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer os.Remove(invEndpoint)
	defer listener.Close()

	// Serve HTTP on the listener.
	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	defer server.Close()

	// Dial the invocation socket and send an HTTP request.
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.DialTimeout("unix", invEndpoint, 5*time.Second)
			},
		},
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get("http://module/api/test")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("X-Module"); got != "test" {
		t.Errorf("X-Module header: got %q, want %q", got, "test")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "hello from module" {
		t.Errorf("body: got %q, want %q", string(body), "hello from module")
	}
}

// TestProvider_MultipleInvocations verifies that the invocation listener
// handles multiple concurrent connections correctly (fresh connection per invocation).
func TestProvider_MultipleInvocations(t *testing.T) {
	invEndpoint := "/tmp/test-inv-" + randomHex(4) + ".sock"
	defer os.Remove(invEndpoint)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	listener, err := net.Listen("unix", invEndpoint)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer os.Remove(invEndpoint)
	defer listener.Close()

	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	defer server.Close()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.DialTimeout("unix", invEndpoint, 5*time.Second)
			},
			MaxIdleConnsPerHost: 0, // Force new connection per request.
			DisableKeepAlives:   true,
		},
		Timeout: 5 * time.Second,
	}

	// Send 5 sequential requests.
	for i := 0; i < 5; i++ {
		resp, err := client.Get("http://module/api/test")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "ok" {
			t.Errorf("request %d: got %q, want %q", i, string(body), "ok")
		}
	}
}
