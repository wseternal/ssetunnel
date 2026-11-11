// Command ssetunnel is the legacy compatibility binary that dispatches
// all server and client commands from a single entry point. New product
// features target cmd/server and cmd/cli; this binary preserves the
// existing command surface for installed services and SSH ProxyCommand
// configurations.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/wseternal/ssetunnel/internal/command"
)

// Version is the application version. Set at build time via -ldflags:
//
//	go build -ldflags "-X main.Version=0.1.0"
var Version = "dev"

// GitSHA is the short git commit SHA. Set at build time via -ldflags:
//
//	go build -ldflags "-X main.GitSHA=$(git rev-parse --short HEAD)"
var GitSHA = ""

func init() {
	command.Version = Version
	command.GitSHA = GitSHA
}

func main() {
	log.SetPrefix("ssetunnel: ")
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, command.Usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "server":
		if len(os.Args) > 2 && command.ServiceActions[os.Args[2]] {
			if handled, err := command.DispatchServiceAction("server", os.Args[2:]); handled {
				if err != nil {
					log.Fatal(err)
				}
				return
			}
		}
		err = command.RunServer(ctx, os.Args[2:])
	case "agent":
		if len(os.Args) > 2 && command.ServiceActions[os.Args[2]] {
			if handled, err := command.DispatchServiceAction("agent", os.Args[2:]); handled {
				if err != nil {
					log.Fatal(err)
				}
				return
			}
		}
		err = command.RunAgent(ctx, os.Args[2:])
	case "connect":
		if len(os.Args) > 2 && command.ServiceActions[os.Args[2]] {
			if handled, err := command.DispatchServiceAction("connect", os.Args[2:]); handled {
				if err != nil {
					log.Fatal(err)
				}
				return
			}
		}
		err = command.RunConnect(ctx, os.Args[2:])
	case "login":
		err = command.RunLogin(ctx, os.Args[2:])
	case "probe":
		err = command.RunProbe(ctx, os.Args[2:])
	case "module":
		err = command.RunModule(os.Args[2:])
	case "version":
		fmt.Println(command.BuildVersion())
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n%s", os.Args[1], command.Usage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}
