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
func startModuleProcess(ctx context.Context, executable, credential, endpoint string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable)
	// Pass only required environment variables (W5 fix: prevent secret leakage).
	cmd.Env = []string{
		"SSETUNNEL_MODULE_IPC_CREDENTIAL=" + credential,
		"SSETUNNEL_MODULE_IPC_ENDPOINT=" + endpoint,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	// Set process group so we can kill all children.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	// Discard stdout/stderr (captured by supervisor logging separately).
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd
}
