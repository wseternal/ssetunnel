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

	orcapostgres "github.com/visdomtech/orcacommon/postgres"
	"github.com/wseternal/ssetunnel/internal/agent"
	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/connect"
	"github.com/wseternal/ssetunnel/internal/consoleserver"
	"github.com/wseternal/ssetunnel/internal/probe"
	"github.com/wseternal/ssetunnel/internal/server"
	"github.com/wseternal/ssetunnel/migrations"
)

const usage = `usage: ssetunnel <command> [flags]

commands:
  server    run the public tunnel server
  agent     run the agent inside the restricted network
  connect   run the user connect client wrapper
  probe     measure a server's POST path (body cap, throttling)
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
	case "connect":
		err = runConnect(ctx, os.Args[2:])
	case "probe":
		err = runProbe(ctx, os.Args[2:])
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
	consoleListen := fs.String("console-listen", ":8081", "HTTP listen address for admin console SPA")
	heartbeat := fs.Duration("heartbeat", 15*time.Second, "SSE heartbeat interval")
	dbURL := fs.String("db-url", os.Getenv("DATABASE_URL"), "PostgreSQL DB connection URL (default uses testcontainer if empty)")
	totpSecret := fs.String("totp-secret", os.Getenv("SSETUNNEL_TOTP_SECRET"), "Admin TOTP secret for console authentication")
	disableAuth := fs.Bool("disable-auth", false, "Disable authentication enforcement")

	if err := fs.Parse(args); err != nil {
		return err
	}

	srv := server.NewServer(*heartbeat)

	var store *auth.Store
	if !*disableAuth {
		targetDBURL := *dbURL
		if targetDBURL == "" {
			targetDBURL = "postgres:tc:"
		}
		dbcfg := orcapostgres.DBConfig{
			DatabaseURLTemplate: targetDBURL,
		}
		pool, err := orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
		if err != nil {
			return fmt.Errorf("open postgres pool: %w", err)
		}
		store = auth.NewStore(pool)
		srv.SetAuthStore(store)

		if *consoleListen != "" {
			consoleHandler := consoleserver.NewConsoleHandler(ctx, pool, store, srv.Reg, *totpSecret)
			consoleSrv := &http.Server{
				Addr:         *consoleListen,
				Handler:      consoleHandler,
				ReadTimeout:  15 * time.Second,
				WriteTimeout: 30 * time.Second,
			}
			go func() {
				<-ctx.Done()
				consoleSrv.Shutdown(context.Background())
			}()
			go func() {
				log.Printf("server: admin console http://localhost%s", *consoleListen)
				if err := consoleSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
					log.Printf("server: console error: %v", err)
				}
			}()
		}
	}

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
	token := fs.String("token", os.Getenv("SSETUNNEL_TOKEN"), "Bearer token or single-use PIN for agent authentication")
	batchSize := fs.Int("batch-size", 16384, "upstream batch ceiling in bytes (1024..1048576)")
	concurrency := fs.Int("concurrency", 1, "upstream POST sender depth (1..4)")
	compress := fs.Bool("compress", false, "negotiate gzip-per-batch upstream encoding")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *serverURL == "" || *target == "" {
		return errors.New("--server and --target are required")
	}
	batch, conc := clampAgentFlags(*batchSize, *concurrency)
	log.Printf("agent: target %s -> server %s (batch-size %d, concurrency %d, compress %v)",
		*target, *serverURL, batch, conc, *compress)

	ag := &agent.Agent{
		ServerURL:   *serverURL,
		Target:      *target,
		Token:       *token,
		BatchSize:   batch,
		Concurrency: conc,
		Compress:    *compress,
	}
	return ag.Run(ctx)
}

func runConnect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	serverEntry := fs.String("server-entry", "127.0.0.1:9090", "tunnel server entry TCP address")
	token := fs.String("token", os.Getenv("SSETUNNEL_TOKEN"), "Bearer token for connection authentication")
	local := fs.String("local", "", "local listen TCP address (e.g. 127.0.0.1:3306) or '-' for Stdio mode")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *local == "" {
		return errors.New("--local is required (e.g. --local 127.0.0.1:3306 or --local -)")
	}

	client := connect.NewClient(*serverEntry, *token)

	if *local == "-" {
		log.Printf("connect: running in Stdio mode connecting to %s", *serverEntry)
		return client.ServeStdio(ctx)
	}

	ln, err := net.Listen("tcp", *local)
	if err != nil {
		return fmt.Errorf("listen local %s: %w", *local, err)
	}
	log.Printf("connect: listening on local port %s -> entry %s", *local, *serverEntry)
	return client.ServeListener(ctx, ln)
}

const (
	minBatchSize = 1024
	maxBatchSize = 1 << 20
	minConc      = 1
	maxConc      = 4
)

func clampAgentFlags(batchSize, concurrency int) (int, int) {
	if batchSize < minBatchSize {
		log.Printf("agent: --batch-size %d below minimum, clamped to %d", batchSize, minBatchSize)
		batchSize = minBatchSize
	}
	if batchSize > maxBatchSize {
		log.Printf("agent: --batch-size %d above maximum, clamped to %d", batchSize, maxBatchSize)
		batchSize = maxBatchSize
	}
	if concurrency < minConc {
		log.Printf("agent: --concurrency %d below minimum, clamped to %d", concurrency, minConc)
		concurrency = minConc
	}
	if concurrency > maxConc {
		log.Printf("agent: --concurrency %d above maximum, clamped to %d", concurrency, maxConc)
		concurrency = maxConc
	}
	return batchSize, concurrency
}

func runProbe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	serverURL := fs.String("server", "", "tunnel server URL, e.g. http://tunnel.example.com")
	if err := fs.Parse(args); err != nil {
		return err
	}
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
