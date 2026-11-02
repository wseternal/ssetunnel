//go:build windows

package agentmod

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

// startModuleProcess creates an exec.Cmd for launching a module process.
// The process is started directly (no shell) with the IPC credential and
// endpoint passed as environment variables.
func startModuleProcess(ctx context.Context, executable, credential, endpoint string) *exec.Cmd {
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
	cmd.Stderr = nil
	return cmd
}
