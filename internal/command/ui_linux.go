package command

import (
	"log"
	"os/exec"
)

func openBrowser(url string) {
	if err := exec.Command("xdg-open", url).Start(); err != nil {
		log.Printf("ui: failed to open browser: %v", err)
	}
}
