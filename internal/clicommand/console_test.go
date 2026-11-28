package clicommand

import (
	"strings"
	"testing"

	"github.com/wseternal/ssetunnel/internal/clientapp"
)

func TestConsoleErrorAuthHintScope(t *testing.T) {
	oldBinaryName := BinaryName
	BinaryName = "cli"
	t.Cleanup(func() { BinaryName = oldBinaryName })

	invokeErr := consoleError(&clientapp.ConsoleStatusError{
		StatusCode: 401,
		Status:     "401 Unauthorized",
		FromInvoke: true,
	})
	if invokeErr == nil {
		t.Fatal("consoleError returned nil for an invoke 401")
	}
	if strings.Contains(invokeErr.Error(), "session was rejected") {
		t.Fatalf("invoke 401 got a session hint: %q", invokeErr)
	}

	catalogErr := consoleError(&clientapp.ConsoleStatusError{
		StatusCode: 401,
		Status:     "401 Unauthorized",
	})
	if catalogErr == nil {
		t.Fatal("consoleError returned nil for a catalog 401")
	}
	if !strings.Contains(catalogErr.Error(), "session was rejected") {
		t.Fatalf("catalog 401 lost the session hint: %q", catalogErr)
	}
}

func TestConsoleDisplayPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/x?token=SECRET", "/x"},
		{"/x#frag", "/x"},
		{"/x?token=SECRET#frag", "/x"},
		{"/x", "/x"},
		{"/?q", "/"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := consoleDisplayPath(tt.path); got != tt.want {
			t.Errorf("consoleDisplayPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
