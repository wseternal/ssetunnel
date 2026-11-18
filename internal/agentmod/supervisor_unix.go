//go:build !windows

package agentmod

import (
	"bufio"
	"context"
	"log"
	"os"
	"os/exec"
	"syscall"
)

// startModuleProcess creates an exec.Cmd for launching a module process.
// The process is started directly (no shell) with a minimal environment
// containing only the IPC credential and endpoint (W5 fix: no env leak).
// Module stderr is captured and forwarded to the agent log with a module prefix.
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
	cmd.Stdout = nil
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("module[%s]: stderr pipe unavailable: %v", moduleID, err)
		return cmd
	}
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("module[%s]: %s", moduleID, scanner.Text())
		}
	}()
	return cmd
}
