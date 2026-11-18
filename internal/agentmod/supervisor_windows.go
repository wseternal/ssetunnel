//go:build windows

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
// The process is started directly (no shell) with the IPC credential and
// endpoint passed as environment variables.
// Module stderr is captured and forwarded to the agent log with a module prefix.
func startModuleProcess(ctx context.Context, executable, credential, endpoint, moduleID string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable)
	cmd.Env = append(os.Environ(),
		"SSETUNNEL_MODULE_IPC_CREDENTIAL="+credential,
		"SSETUNNEL_MODULE_IPC_ENDPOINT="+endpoint,
	)
	// On Windows, we use CREATE_NEW_PROCESS_GROUP to isolate the process.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	cmd.Stdout = nil
	stderr, _ := cmd.StderrPipe()
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("module[%s]: %s", moduleID, scanner.Text())
		}
	}()
	return cmd
}
