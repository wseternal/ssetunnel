// Command server runs the public tunnel server that accepts agent
// connections and routes client sessions to them.
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
	args := os.Args[1:]
	// Services installed by the legacy ssetunnel binary invoke
	// "server run <flags>"; accept that argv shape so an in-place
	// binary upgrade keeps the installed service working.
	if len(args) >= 2 && args[0] == "server" && args[1] == "run" {
		args = args[2:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: server [flags]")
		fmt.Fprintln(os.Stderr, "  or: server <action> [flags]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "actions: run, start, stop, restart, status, reload, uninstall, version")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Run 'server --help' for the full flag list.")
		os.Exit(2)
	}
	if args[0] == "version" {
		fmt.Println(command.BuildVersion())
		return
	}
	if len(args) > 0 && command.ServiceActions[args[0]] {
		if handled, err := command.DispatchServiceActionBare("server", args, serverRunFnBuilder); handled {
			if err != nil {
				log.Fatal(err)
			}
			return
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := command.RunServer(ctx, args); err != nil {
		log.Fatal(err)
	}
}

// serverRunFnBuilder builds the run function for the server service.
// It strips --service-user (a service-installer flag, not a runtime flag)
// before passing args to RunServer.
func serverRunFnBuilder(_ string, args []string) func(context.Context) error {
	filtered := command.StripFlag(args, "--service-user")
	return func(ctx context.Context) error { return command.RunServer(ctx, filtered) }
}
