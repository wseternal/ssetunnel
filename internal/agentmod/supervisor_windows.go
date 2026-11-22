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
// Both output streams are forwarded to the agent log with a module prefix;
// stdout is tagged so its lines stay distinguishable.
func startModuleProcess(ctx context.Context, executable, credential, endpoint, moduleID string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable)
	cmd.Env = append(os.Environ(),
		"SSETUNNEL_MODULE_IPC_CREDENTIAL="+credential,
		"SSETUNNEL_MODULE_IPC_ENDPOINT="+endpoint,
		"SSETUNNEL_MODULE_ID="+moduleID,
	)
	// On Windows, we use CREATE_NEW_PROCESS_GROUP to isolate the process.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	cmd.Stdout = &moduleLogWriter{moduleID: moduleID, stream: " out"}
	cmd.Stderr = &moduleLogWriter{moduleID: moduleID}
	// No WaitDelay: the supervisor waits on the *os.Process, not on this
	// exec.Cmd, so exec's pipe bookkeeping never runs and the field would be
	// inert (see registry.stopProcess).
	return cmd
}
