package clicommand

import (
	"log"
	"os/exec"
)

func openBrowser(url string) {
	cmd := exec.Command("cmd", "/c", "start", url)
	go func() {
		if err := cmd.Run(); err != nil {
			log.Printf("ui: failed to open browser: %v", err)
		}
	}()
}
