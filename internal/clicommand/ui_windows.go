package clicommand

import (
	"log"
	"os/exec"
)

func openBrowser(url string) {
	if err := exec.Command("cmd", "/c", "start", url).Start(); err != nil {
		log.Printf("ui: failed to open browser: %v", err)
	}
}
