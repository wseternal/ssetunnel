//go:build !windows

package agentmod

import (
	"fmt"
	"os"
)

// validateExecutableMode rejects files the platform cannot execute. On Unix
// that is the absence of any executable bit.
func validateExecutableMode(path string, info os.FileInfo) error {
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("executable is not executable")
	}
	return nil
}
