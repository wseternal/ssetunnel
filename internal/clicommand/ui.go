package clicommand

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/wseternal/ssetunnel/internal/clientserver"
)

// RunUI starts the secured local browser server for the client UI.
func RunUI(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:0", "loopback listen address (default: random port)")
	noOpen := fs.Bool("no-open", false, "do not open the browser automatically")
	if err := fs.Parse(args); err != nil {
		return err
	}

	srv, err := clientserver.New(clientserver.Config{
		Listen: *listen,
	})
	if err != nil {
		return fmt.Errorf("create ui server: %w", err)
	}

	url := srv.URL()
	log.Printf("ui: listening on %s", srv.Addr())

	if !*noOpen {
		fmt.Printf("Opening %s in your browser.\n", url)
		// Platform-specific open is best-effort; do not fail the command.
		openBrowser(url)
	} else {
		fmt.Printf("Open %s in your browser.\n", url)
	}

	return srv.Serve(ctx)
}
