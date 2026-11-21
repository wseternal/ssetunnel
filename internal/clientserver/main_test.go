package clientserver

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points HOME at a temporary directory for the whole test binary.
// The status file path follows HOME, and in-process agents write their PID
// into that file. Without this isolation, a status read that runs before a
// test installs its own HOME resolves the developer's real ~/.ssetunnel, so
// every later in-process agent writes into the real file, where a second
// test run or a running agent finds the PID and sends SIGTERM.
//
// The directory name stays short because the module IPC socket lives at
// <HOME>/.ssetunnel/agent/module.sock, and that path must fit macOS's
// 104-byte sun_path limit.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "ssetui")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp home: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("HOME", home); err != nil {
		fmt.Fprintf(os.Stderr, "set HOME: %v\n", err)
		_ = os.RemoveAll(home)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
