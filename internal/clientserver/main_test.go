package clientserver

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points the home directory at a temporary location for the whole
// test binary. The status file path follows the home directory, and in-process
// agents write their PID into that file. Without this isolation, a status read
// that runs before a test installs its own home resolves the developer's real
// ~/.ssetunnel, so every later in-process agent writes into the real file,
// where a second test run or a running agent finds the PID and sends SIGTERM.
// The session file follows the same lookup, so an isolated run also leaves the
// developer's saved session untouched.
//
// HOME covers Unix and USERPROFILE covers Windows, because os.UserHomeDir
// reads a different variable on each platform.
//
// The directory name stays short because the module IPC socket lives at
// <home>/.ssetunnel/agent/module.sock, and that path must fit macOS's
// 104-byte sun_path limit.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "ssetui")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp home: %v\n", err)
		os.Exit(1)
	}
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if err := os.Setenv(key, home); err != nil {
			fmt.Fprintf(os.Stderr, "set %s: %v\n", key, err)
			_ = os.RemoveAll(home)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
