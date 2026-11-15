package clicommand

import (
	"context"
	"fmt"

	"github.com/wseternal/ssetunnel/internal/command"
)

// BuildRunFn returns a closure that dispatches to the appropriate
// client command (agent, connect, etc.) with the given args.
// This is the client-side counterpart to the server's RunServer.
// Note: --service-user may still be present in args when called
// outside DispatchServiceAction (e.g. directly from cmd/cli), so
// it is stripped here defensively. When called via DispatchServiceAction,
// the strip is idempotent (--service-user was already extracted).
func BuildRunFn(subcommand string, args []string) func(context.Context) error {
	filtered := command.StripFlag(args, "--service-user")
	switch subcommand {
	case "agent":
		return func(ctx context.Context) error { return RunAgent(ctx, filtered) }
	case "connect":
		return func(ctx context.Context) error { return RunConnect(ctx, filtered) }
	default:
		return func(context.Context) error {
			return fmt.Errorf("unsupported subcommand for service: %s", subcommand)
		}
	}
}
