//go:build windows

package agentmod

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// validateExecutableMode rejects files Windows cannot execute. Windows has no
// executable permission bit: os.FileInfo reports 0666 for every regular file,
// so a mode test would reject every module. The extension carries the same
// information, and os/exec requires it.
func validateExecutableMode(path string, _ os.FileInfo) error {
	if !strings.EqualFold(filepath.Ext(path), ".exe") {
		return fmt.Errorf("executable must be a .exe file on Windows")
	}
	return nil
}
