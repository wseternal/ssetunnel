// Command ssetunnel exposes private TCP services through a public server
// over an SSE-down + batched-POST-up transport, for agents stuck behind
// proxies that allow only plain outbound HTTP(S).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
  login     authenticate and store a session for agent/connect
  probe     measure a server's POST path (body cap, throttling)
  version   print the build version and git revision
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
	case "login":
		err = runLogin(ctx, os.Args[2:])
	case "probe":
		err = runProbe(ctx, os.Args[2:])
	case "version":
		fmt.Println(BuildVersion())
		return
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
	consoleListen := fs.String("console-listen", ":8081", "HTTP listen address for admin console SPA")
	heartbeat := fs.Duration("heartbeat", 15*time.Second, "SSE heartbeat interval")
	dbURL := fs.String("db-url", os.Getenv("DATABASE_URL"), "PostgreSQL DB connection URL (default uses testcontainer if empty)")
	disableAuth := fs.Bool("disable-auth", false, "Disable authentication enforcement")
	// Accept --totp-secret for backward compatibility (silently ignored).
	_ = fs.String("totp-secret", "", "DEPRECATED: per-user TOTP is now used; this flag is ignored")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Deprecation warning for removed global TOTP secret.
	if os.Getenv("SSETUNNEL_TOTP_SECRET") != "" {
		log.Println("server: WARNING: SSETUNNEL_TOTP_SECRET is deprecated; per-user TOTP is now used. Set up TOTP via the console.")
	}

	srv := server.NewServer(*heartbeat)

	var store *auth.Store
	var consoleLn net.Listener
	var pool *pgxpool.Pool
	if !*disableAuth {
		targetDBURL := *dbURL
		if targetDBURL == "" {
			targetDBURL = "postgres:tc:"
		}
		dbcfg := orcapostgres.DBConfig{
			DatabaseURLTemplate: targetDBURL,
		}
		var err error
		pool, err = orcapostgres.OpenPool(ctx, dbcfg, orcapostgres.NewMigrator(migrations.FS, nil))
		if err != nil {
			return fmt.Errorf("open postgres pool: %w", err)
		}
		store = auth.NewStore(pool)

		// Configure HMAC pepper for recovery code digests (if set).
		if pepper := os.Getenv("SSETUNNEL_RECOVERY_CODE_PEPPER"); pepper != "" {
			store.SetRecoveryCodePepper(pepper)
		} else {
			log.Println("server: WARNING: SSETUNNEL_RECOVERY_CODE_PEPPER is not set; recovery codes use SHA-256 digests. Set this env var for stronger security.")
		}

		srv.SetAuthStore(store)

		// Seed an admin user on first startup so the console is accessible.
		if adminPW, err := store.EnsureAdminUser(ctx); err != nil {
			if consoleLn != nil {
				consoleLn.Close()
			}
			return fmt.Errorf("ensure admin user: %w", err)
		} else if adminPW != "" {
			log.Printf("server: no admin user found — created 'admin' with password: %s", adminPW)
			log.Printf("server: ⚠  change this password immediately via the console")
		}

		// Warn if no users have TOTP enrolled (global TOTP was removed).
		if anyTOTP, err := store.AnyTOTPEnrolled(ctx); err == nil && !anyTOTP {
			log.Println("server: no users have TOTP enrolled — per-user TOTP must be set up via the console for two-factor authentication")
		}

		if *consoleListen != "" {
			var err error
			consoleLn, err = net.Listen("tcp", *consoleListen)
			if err != nil {
				return fmt.Errorf("listen console %s: %w", *consoleListen, err)
			}
		}
	}

	// Open the HTTP listener eagerly so address-in-use errors surface
	// before we start serving.
	httpLn, err := net.Listen("tcp", *listen)
	if err != nil {
		if consoleLn != nil {
			consoleLn.Close()
		}
		return fmt.Errorf("listen http %s: %w", *listen, err)
	}

	if consoleLn != nil {
		consoleHandler := consoleserver.NewConsoleHandler(ctx, pool, store, srv.Reg)
		consoleSrv := &http.Server{
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
			if err := consoleSrv.Serve(consoleLn); !errors.Is(err, http.ErrServerClosed) {
				log.Printf("server: console error: %v", err)
			}
		}()
	}

	httpSrv := srv.NewHTTPServer(*listen)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx)
	}()

	log.Printf("server: ssetunnel %s", BuildVersion())
	log.Printf("server: http %s", *listen)
	if err := httpSrv.Serve(httpLn); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve http: %w", err)
	}
	return nil
}

func runAgent(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	serverURL := fs.String("server", "http://127.0.0.1:8080", "tunnel server URL")
	target := fs.String("target", "", "TCP address to forward streams to (empty = dynamic target mode)")
	agentID := fs.String("id", "", "agent identifier for routing (e.g. mydevbox)")
	batchSize := fs.Int("batch-size", 16384, "upstream batch ceiling in bytes (1024..1048576)")
	concurrency := fs.Int("concurrency", 1, "upstream POST sender depth (1..4)")
	compress := fs.Bool("compress", false, "negotiate gzip-per-batch upstream encoding")

	if err := fs.Parse(args); err != nil {
		return err
	}
	batch, conc := clampAgentFlags(*batchSize, *concurrency)

	// Load session token from ~/.ssetunnel/session
	sessToken, sessErr := auth.LoadSession()
	if sessErr != nil {
		log.Printf("agent: warning: failed to load session: %v", sessErr)
	} else if sessToken != "" {
		log.Printf("agent: using session from ~/.ssetunnel/session")
	}

	// Build request modifier for session-based auth
	var reqMod func(*http.Request)
	if sessToken != "" {
		token := sessToken // capture for closure
		reqMod = func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	log.Printf("agent: ssetunnel %s", BuildVersion())
	if *target != "" {
		log.Printf("agent: target %s -> server %s (id=%s, batch-size %d, concurrency %d, compress %v)",
			*target, *serverURL, *agentID, batch, conc, *compress)
	} else {
		log.Printf("agent: dynamic target mode -> server %s (id=%s, batch-size %d, concurrency %d, compress %v)",
			*serverURL, *agentID, batch, conc, *compress)
	}

	ag := &agent.Agent{
		ServerURL:       *serverURL,
		Target:          *target,
		AgentID:         *agentID,
		Token:           sessToken,
		RequestModifier: reqMod,
		BatchSize:       batch,
		Concurrency:     conc,
		Compress:        *compress,
	}
	return ag.Run(ctx)
}

func runConnect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	serverURL := fs.String("server", "http://127.0.0.1:8080", "tunnel server URL")
	fs.StringVar(serverURL, "server-agent", "", "DEPRECATED: use -server")
	fs.StringVar(serverURL, "server-entry", "", "DEPRECATED: use -server")
	agentID := fs.String("agent", "", "agent identifier to connect to (e.g. mydevbox)")
	target := fs.String("target", "", "target address on the agent machine (e.g. 127.0.0.1:22)")
	local := fs.String("local", "", "local listen TCP address (e.g. 127.0.0.1:3306) or '-' for Stdio mode")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *local == "" {
		return errors.New("--local is required (e.g. --local 127.0.0.1:3306 or --local -)")
	}

	// Auto-detect legacy TCP addresses (no http:// or https:// prefix)
	// and convert to HTTP URL for backward compatibility.
	url := *serverURL
	if url != "" && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		log.Printf("connect: WARNING: %q looks like a TCP address; converting to http://%s (the TCP entry listener is removed)", url, url)
		url = "http://" + url
	}

	// Load session token from ~/.ssetunnel/session
	sessToken, sessErr := auth.LoadSession()
	if sessErr != nil {
		log.Printf("connect: warning: failed to load session: %v", sessErr)
	} else if sessToken != "" {
		log.Printf("connect: using session from ~/.ssetunnel/session")
	}

	client := connect.NewClient(url, sessToken, *agentID, *target)

	if *local == "-" {
		log.Printf("connect: running in Stdio mode connecting to %s (agent=%s, target=%s)", url, *agentID, *target)
		return client.ServeStdio(ctx)
	}

	ln, err := net.Listen("tcp", *local)
	if err != nil {
		return fmt.Errorf("listen local %s: %w", *local, err)
	}
	log.Printf("connect: listening on local port %s -> server %s (agent=%s, target=%s)", *local, url, *agentID, *target)
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

func runLogin(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	consoleURL := fs.String("console", "http://127.0.0.1:8081", "console API URL")
	if err := fs.Parse(args); err != nil {
		return err
	}

	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Username: ")
	username, _ := reader.ReadString('\n')
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("username is required")
	}

	fmt.Print("Password: ")
	password, _ := reader.ReadString('\n')
	password = strings.TrimSpace(password)
	if password == "" {
		return errors.New("password is required")
	}

	// Check if TOTP is required for this user.
	var totpCode string
	checkBody, _ := json.Marshal(map[string]string{"username": username})
	checkResp, err := http.Post(*consoleURL+"/api/v1/user-login-check", "application/json", strings.NewReader(string(checkBody)))
	if err == nil {
		defer checkResp.Body.Close()
		if checkResp.StatusCode == http.StatusOK {
			var check struct {
				TOTPRequired bool `json:"totp_required"`
			}
			if json.NewDecoder(checkResp.Body).Decode(&check) == nil && check.TOTPRequired {
				fmt.Print("TOTP or Recovery Code: ")
				totpCode, _ = reader.ReadString('\n')
				totpCode = strings.TrimSpace(totpCode)
			}
		} else {
			// Non-200 status: still allow login attempt.
			fmt.Print("TOTP or Recovery Code (press Enter to skip): ")
			totpCode, _ = reader.ReadString('\n')
			totpCode = strings.TrimSpace(totpCode)
		}
	} else {
		// If check endpoint is unavailable, still allow login attempt.
		fmt.Print("TOTP or Recovery Code (press Enter to skip): ")
		totpCode, _ = reader.ReadString('\n')
		totpCode = strings.TrimSpace(totpCode)
	}

	// Build request
	reqBody, _ := json.Marshal(map[string]string{
		"username":  username,
		"password":  password,
		"totp_code": totpCode,
	})

	resp, err := http.Post(*consoleURL+"/api/v1/user-login", "application/json", strings.NewReader(string(reqBody)))
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("login failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if err := auth.SaveSession(result.Token); err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	fmt.Printf("Login successful! Session saved for %s (role: %s)\n", result.Username, result.Role)
	fmt.Println("You can now run 'ssetunnel agent' or 'ssetunnel connect' without --token.")
	return nil
}
