// Command cli provides the client-side tunnel workflows: login, agent,
// connect, probe, module management, and version information.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/wseternal/ssetunnel/internal/clicommand"
	"github.com/wseternal/ssetunnel/internal/command"
)

// Version is the application version. Set at build time via -ldflags.
var Version = "dev"

// GitSHA is the short git commit SHA. Set at build time via -ldflags.
var GitSHA = ""

func init() {
	command.Version = Version
	command.GitSHA = GitSHA
	clicommand.BinaryName = "cli"
}

func main() {
	log.SetPrefix("cli: ")
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, command.CLIUsage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "login":
		err = clicommand.RunLogin(ctx, os.Args[2:])
	case "agent":
		if len(os.Args) > 2 && command.ServiceActions[os.Args[2]] {
			if handled, err := command.DispatchServiceAction("agent", os.Args[2:], clicommand.BuildRunFn); handled {
				if err != nil {
					log.Fatal(err)
				}
				return
			}
		}
		err = clicommand.RunAgent(ctx, os.Args[2:])
	case "connect":
		if len(os.Args) > 2 && command.ServiceActions[os.Args[2]] {
			if handled, err := command.DispatchServiceAction("connect", os.Args[2:], clicommand.BuildRunFn); handled {
				if err != nil {
					log.Fatal(err)
				}
				return
			}
		}
		err = clicommand.RunConnect(ctx, os.Args[2:])
	case "probe":
		err = clicommand.RunProbe(ctx, os.Args[2:])
	case "module":
		err = clicommand.RunModule(os.Args[2:])
	case "version":
		fmt.Println(command.BuildVersion())
		return
	case "ui":
		err = clicommand.RunUI(ctx, os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n%s", os.Args[1], command.CLIUsage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}
