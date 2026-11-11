// Command server runs the public tunnel server that accepts agent
// connections and routes client sessions to them.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/wseternal/ssetunnel/internal/command"
)

// Version is the application version. Set at build time via -ldflags.
var Version = "dev"

// GitSHA is the short git commit SHA. Set at build time via -ldflags.
var GitSHA = ""

func init() {
	command.Version = Version
	command.GitSHA = GitSHA
}

func main() {
	log.SetPrefix("server: ")
	if len(os.Args) > 1 && command.ServiceActions[os.Args[1]] {
		if handled, err := command.DispatchServiceAction("server", os.Args[1:]); handled {
			if err != nil {
				log.Fatal(err)
			}
			return
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := command.RunServer(ctx, os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
