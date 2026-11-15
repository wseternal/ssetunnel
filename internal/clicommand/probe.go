package clicommand

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/wseternal/ssetunnel/internal/command"
	"github.com/wseternal/ssetunnel/internal/probe"
)

// RunProbe measures a server's POST path (body cap, throttling) and
// recommends batch-size and concurrency settings.
func RunProbe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	serverURL := fs.String("server", "", "tunnel server URL, e.g. http://tunnel.example.com")
	base := command.AcceptDeprecatedBaseFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	command.WarnDeprecatedBase("probe", *base)
	if *serverURL == "" {
		return errors.New("--server is required")
	}
	rep, err := probe.Run(ctx, probe.Config{URL: *serverURL})
	if err != nil {
		return err
	}
	fmt.Print(rep.String())
	return nil
}
