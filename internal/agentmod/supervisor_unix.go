//go:build !windows

package agentmod

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

// startModuleProcess creates an exec.Cmd for launching a module process.
// The process is started directly (no shell) with a minimal environment
// containing only the IPC credential and endpoint (W5 fix: no env leak).
// Both output streams are forwarded to the agent log with a module prefix;
// stdout is tagged so its lines stay distinguishable.
func startModuleProcess(ctx context.Context, executable, credential, endpoint, moduleID string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable)
	// Pass only required environment variables (W5 fix: prevent secret leakage).
	cmd.Env = []string{
		"SSETUNNEL_MODULE_IPC_CREDENTIAL=" + credential,
		"SSETUNNEL_MODULE_IPC_ENDPOINT=" + endpoint,
		"SSETUNNEL_MODULE_ID=" + moduleID,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	// Set process group so we can kill all children.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	cmd.Stdout = &moduleLogWriter{moduleID: moduleID, stream: " out"}
	cmd.Stderr = &moduleLogWriter{moduleID: moduleID}
	// No WaitDelay: the supervisor waits on the *os.Process, not on this
	// exec.Cmd, so exec's pipe bookkeeping never runs and the field would be
	// inert (see registry.stopProcess).
	return cmd
}
