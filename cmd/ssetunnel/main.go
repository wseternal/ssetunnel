// Command ssetunnel exposes private TCP services through a public server
// over an SSE-down + batched-POST-up transport, for agents stuck behind
// proxies that allow only plain outbound HTTP(S).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wseternal/ssetunnel/internal/agent"
	"github.com/wseternal/ssetunnel/internal/server"
)

const usage = `usage: ssetunnel <command> [flags]

commands:
  server    run the public tunnel server
  agent     run the agent inside the restricted network
`

func main() {
	log.SetPrefix("ssetunnel: ")
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "server":
		err = runServer(ctx, os.Args[2:])
	case "agent":
		err = runAgent(ctx, os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func runServer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	listen := fs.String("listen", ":8080", "HTTP listen address for tunnel endpoints")
	entry := fs.String("entry", ":9090", "TCP entry listen address for users")
	heartbeat := fs.Duration("heartbeat", 15*time.Second, "SSE heartbeat interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	srv := server.NewServer(*heartbeat)
	entryLn, err := net.Listen("tcp", *entry)
	if err != nil {
		return fmt.Errorf("listen entry %s: %w", *entry, err)
	}
	go func() {
		if err := srv.ServeEntry(ctx, entryLn); err != nil {
			log.Printf("server: entry listener: %v", err)
		}
	}()
	httpSrv := srv.NewHTTPServer(*listen)
	go func() {
		<-ctx.Done()
		httpSrv.Shutdown(context.Background())
	}()
	log.Printf("server: http %s, entry %s", *listen, *entry)
	if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve http: %w", err)
	}
	return nil
}

func runAgent(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	serverURL := fs.String("server", "", "tunnel server URL, e.g. http://tunnel.example.com")
	target := fs.String("target", "", "TCP address to forward streams to, e.g. 127.0.0.1:3000")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *serverURL == "" || *target == "" {
		return errors.New("--server and --target are required")
	}
	log.Printf("agent: server %s, target %s", *serverURL, *target)
	ag := &agent.Agent{ServerURL: *serverURL, Target: *target}
	return ag.Run(ctx)
}
