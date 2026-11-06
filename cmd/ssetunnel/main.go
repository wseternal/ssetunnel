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
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	orcapostgres "github.com/visdomtech/orcacommon/postgres"
	"github.com/wseternal/ssetunnel/internal/agent"
	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/connect"
	"github.com/wseternal/ssetunnel/internal/consoleserver"
	"github.com/wseternal/ssetunnel/internal/metrics"
	"github.com/wseternal/ssetunnel/internal/probe"
	"github.com/wseternal/ssetunnel/internal/server"
	"github.com/wseternal/ssetunnel/migrations"
)

const usage = `usage: ssetunnel <command> [flags]

commands:
  server    run the public tunnel server
            service actions: run, start, stop, restart, status, reload, uninstall
  agent     run the agent inside the restricted network
            service actions: run, start, stop, restart, status, reload, uninstall
  connect   run the user connect client wrapper
            service actions: run, start, stop, restart, status, uninstall
  login     authenticate and store a session for agent/connect
  probe     measure a server's POST path (body cap, throttling)
  module    manage agent modules (install, start, stop, restart, status, remove, list)
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
		if len(os.Args) > 2 && serviceActions[os.Args[2]] {
			if handled, err := dispatchServiceAction("server", os.Args[2:]); handled {
				if err != nil {
					log.Fatal(err)
				}
				return
			}
		}
		err = runServer(ctx, os.Args[2:])
	case "agent":
		if len(os.Args) > 2 && serviceActions[os.Args[2]] {
			if handled, err := dispatchServiceAction("agent", os.Args[2:]); handled {
				if err != nil {
					log.Fatal(err)
				}
				return
			}
		}
		err = runAgent(ctx, os.Args[2:])
	case "connect":
		if len(os.Args) > 2 && serviceActions[os.Args[2]] {
			if handled, err := dispatchServiceAction("connect", os.Args[2:]); handled {
				if err != nil {
					log.Fatal(err)
				}
				return
			}
		}
		err = runConnect(ctx, os.Args[2:])
	case "login":
		err = runLogin(ctx, os.Args[2:])
	case "probe":
		err = runProbe(ctx, os.Args[2:])
	case "module":
		err = runModule(os.Args[2:])
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

// acceptDeprecatedBaseFlag keeps --base parseable after route prefixing moved
// to the reverse proxy. A package upgrade does not rewrite an installed unit
// file, so a service started with --base keeps passing it on every restart;
// rejecting the flag would abort the daemon into a restart loop that a bare
// `start` then re-poisons from the saved args file. The value is read back only
// so the caller can say it no longer does anything.
func acceptDeprecatedBaseFlag(fs *flag.FlagSet) *string {
	return fs.String("base", "", "DEPRECATED: ignored — path prefixing is delegated to the reverse proxy")
}

func warnDeprecatedBase(prefix, base string) {
	if base == "" {
		return
	}
	log.Printf("%s: WARNING: --base %q is ignored. Strip the prefix at the reverse proxy (Caddy `handle_path`) and drop --base from this service's arguments.", prefix, base)
}

func runServer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	listen := fs.String("listen", ":8080", "HTTP listen address for tunnel endpoints")
	consoleListen := fs.String("console-listen", ":8081", "HTTP listen address for admin console SPA")
	heartbeat := fs.Duration("heartbeat", 15*time.Second, "SSE heartbeat interval")
	dbURL := fs.String("db-url", os.Getenv("DATABASE_URL"), "PostgreSQL DB connection URL (default uses testcontainer if empty)")
	// Default metrics directory: ~/.ssetunnel/metrics (empty = metrics disabled).
	var defaultMetricsDir string
	if home, err := os.UserHomeDir(); err == nil {
		defaultMetricsDir = filepath.Join(home, ".ssetunnel", "metrics")
	}

	disableAuth := fs.Bool("disable-auth", false, "Disable authentication enforcement")
	metricsDir := fs.String("metrics-dir", defaultMetricsDir, "Directory for BadgerDB metrics storage (empty = metrics disabled)")
	metricsRetention := fs.Duration("metrics-retention", 7*24*time.Hour, "How long to retain metrics data (default: 7 days)")
	metricsFlush := fs.Duration("metrics-flush", 10*time.Second, "Interval for flushing metrics to disk (default: 10s)")
	tunerInterval := fs.Duration("tuner-interval", 30*time.Second, "Interval for auto-tuner evaluation (default: 30s)")
	// Accept --totp-secret for backward compatibility (silently ignored).
	_ = fs.String("totp-secret", "", "DEPRECATED: per-user TOTP is now used; this flag is ignored")
	base := acceptDeprecatedBaseFlag(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}
	warnDeprecatedBase("server", *base)

	// Deprecation warning for removed global TOTP secret.
	if os.Getenv("SSETUNNEL_TOTP_SECRET") != "" {
		log.Println("server: WARNING: SSETUNNEL_TOTP_SECRET is deprecated; per-user TOTP is now used. Set up TOTP via the console.")
	}

	// SIGHUP: trigger configuration reload (placeholder).
	installSIGHUPHandler("server", "SIGHUP received, reloading configuration...")
	// TODO: re-read config files, refresh auth pepper, etc.

	srv := server.NewServer(*heartbeat)

	// Metrics and auto-tuner setup (optional, enabled by --metrics-dir).
	if *metricsDir != "" {
		metricsStore, err := metrics.OpenStore(*metricsDir)
		if err != nil {
			return fmt.Errorf("open metrics store: %w", err)
		}
		mc := metrics.NewCollector(metricsStore, *metricsFlush, *metricsRetention)
		srv.SetMetricsCollector(mc)

		// Auto-tuner: evaluates agents and pushes parameter changes via SSE.
		pushFn := func(agentID string, params metrics.TransportParams) error {
			sess := srv.FindSession(agentID)
			if sess == nil {
				return fmt.Errorf("agent %s not found", agentID)
			}
			sess.SendTune(params)
			return nil
		}
		tuner := metrics.NewAutoTuner(mc, metricsStore, pushFn, *tunerInterval)
		go tuner.Run(ctx)

		// Cleanup on shutdown.
		go func() {
			<-ctx.Done()
			mc.Close()
			_ = metricsStore.Close()
		}()
		log.Printf("server: metrics enabled, dir=%s retention=%s", *metricsDir, *metricsRetention)
	}

	var store *auth.Store
	var consoleLn net.Listener
	var pool *pgxpool.Pool
	if !*disableAuth {
		targetDBURL := *dbURL
		if targetDBURL == "" {
			targetDBURL = "postgres:tc:"
		}

		// Embedded/testcontainer postgres cannot run as root because
		// PostgreSQL's initdb refuses to initialise a data directory
		// owned by the superuser. Fail early with a clear message.
		if os.Getuid() == 0 && isEmbeddedPostgres(targetDBURL) {
			return fmt.Errorf("embedded postgres cannot run as root (initdb restriction); " +
				"either run as a non-root user (recommended: ssetunnel server start --service-user ssetunnel) " +
				"or set --db-url to an external PostgreSQL instance")
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
	}

	if *consoleListen != "" {
		var err error
		consoleLn, err = net.Listen("tcp", *consoleListen)
		if err != nil {
			return fmt.Errorf("listen console %s: %w", *consoleListen, err)
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
		consoleHandler := consoleserver.NewConsoleHandler(ctx, store, srv.Reg, srv.MetricsCollector(), srv)
		consoleSrv := &http.Server{
			Handler:      consoleHandler,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 0, // must not kill SSE (cloud shell streams indefinitely)
		}
		// net/http answers OPTIONS * itself, before the console handler, so that
		// response would bypass the security headers the router declares.
		consoleserver.ApplyServerDefaults(consoleSrv)
		go func() {
			<-ctx.Done()
			consoleSrv.Shutdown(context.Background())
		}()
		go func() {
			log.Printf("server: admin console http://localhost%s/", *consoleListen)
			if err := consoleSrv.Serve(consoleLn); !errors.Is(err, http.ErrServerClosed) {
				log.Printf("server: console error: %v", err)
			}
		}()
	}

	httpSrv := srv.NewHTTPServer(*listen)
	// Start persistent shell session cleanup loop.
	shellCleanupDone := make(chan struct{})
	srv.StartShellCleanup(shellCleanupDone)
	go func() {
		<-ctx.Done()
		close(shellCleanupDone)
		// Force-close all active agent sessions before draining HTTP.
		srv.CloseShellSessions()
		srv.Reg.CloseAll()
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
	serverURL := fs.String("server", "", "tunnel server URL (default: from saved session)")
	target := fs.String("target", "", "TCP address to forward streams to (empty = dynamic target mode)")
	agentID := fs.String("id", "", "agent identifier for routing (e.g. mydevbox)")
	batchSize := fs.Int("batch-size", 262144, "upstream batch ceiling in bytes (1024..1048576)")
	concurrency := fs.Int("concurrency", 1, "upstream POST sender depth (1..4)")
	compress := fs.Bool("compress", false, "negotiate gzip-per-batch upstream encoding")
	noAutoTune := fs.Bool("no-auto-tune", false, "disable server auto-tuning (keep static CLI flags)")
	verbose := fs.Bool("verbose", false, "enable detailed shell/control tracing logs")
	base := acceptDeprecatedBaseFlag(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}
	warnDeprecatedBase("agent", *base)
	batch, conc := clampAgentFlags(*batchSize, *concurrency)

	// Resolve server URL and session token.
	url, sessToken, consoleURL, err := resolveServerURL(*serverURL, "agent")
	if err != nil {
		return err
	}

	// Build request modifier for session-based auth.
	// Use a shared pointer so the RequestModifier, OnTokenUpgrade, and
	// OnTokenRefresh all observe the latest token value.
	var reqMod func(*http.Request)
	var tokenPtr *string
	if sessToken != "" {
		tokenPtr = &sessToken
		reqMod = func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+*tokenPtr)
		}
	}

	// Build mid-lifecycle token refresh callback.
	// Called before each reconnect to keep the session fresh during
	// long-running agent sessions.
	var refreshFn func() (string, error)
	if tokenPtr != nil && consoleURL != "" {
		serverKey := url // capture session file key
		cURL := consoleURL
		refreshFn = func() (string, error) {
			// Load current expiry from session file to check if refresh is needed.
			_, _, _, expiresAt, loadErr := auth.LoadSession(serverKey)
			if loadErr != nil {
				log.Printf("agent: warning: cannot read session file: %v", loadErr)
				return *tokenPtr, nil
			}
			if !auth.NeedsRefresh(expiresAt) {
				return *tokenPtr, nil // not due yet
			}
			newTok, newExp, err := auth.RefreshSession(cURL, *tokenPtr)
			if err != nil {
				return "", err
			}
			if saveErr := auth.UpdateSessionToken(serverKey, newTok, newExp); saveErr != nil {
				log.Printf("agent: warning: failed to save refreshed session: %v", saveErr)
			}
			*tokenPtr = newTok // update shared pointer for RequestModifier
			return newTok, nil
		}
	}

	// SIGHUP: trigger configuration reload (placeholder; agent has no
	// runtime-reloadable config today — logs only).
	installSIGHUPHandler("agent", "SIGHUP received (no reloadable config)")

	log.Printf("agent: ssetunnel %s", BuildVersion())
	if *target != "" {
		log.Printf("agent: target %s -> server %s (id=%s, batch-size %d, concurrency %d, compress %v)",
			*target, url, *agentID, batch, conc, *compress)
	} else {
		log.Printf("agent: dynamic target mode -> server %s (id=%s, batch-size %d, concurrency %d, compress %v)",
			url, *agentID, batch, conc, *compress)
	}

	ag := &agent.Agent{
		ServerURL:       url,
		Target:          *target,
		AgentID:         *agentID,
		Token:           sessToken,
		RequestModifier: reqMod,
		BatchSize:       batch,
		Concurrency:     conc,
		Compress:        *compress,
		NoAutoTune:      *noAutoTune,
		Verbose:         *verbose,
		OnTokenRefresh:  refreshFn,
	}

	// Initialize the module subsystem (registry, IPC, supervisor).
	dataDir, dirErr := agentDataDir()
	if dirErr != nil {
		return dirErr
	}
	if err := ag.InitModules(ctx, dataDir); err != nil {
		return fmt.Errorf("init modules: %w", err)
	}

	return ag.Run(ctx)
}

func runConnect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	server := fs.String("server", "", "tunnel server URL (default: from saved session)")
	agentID := fs.String("agent", "", "agent identifier to connect to (e.g. mydevbox)")
	target := fs.String("target", "", "target address on the agent machine (e.g. 127.0.0.1:22)")
	local := fs.String("local", "", "local listen TCP address (e.g. 127.0.0.1:3306) or '-' for Stdio mode")
	batchSize := fs.Int("batch-size", 0, "upstream batch ceiling in bytes (0 = 256 KiB default; 1024..1048576)")
	base := acceptDeprecatedBaseFlag(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}
	warnDeprecatedBase("connect", *base)
	if *local == "" {
		return errors.New("--local is required (e.g. --local 127.0.0.1:3306 or --local -)")
	}

	// Resolve server URL and session token.
	url, sessToken, _, err := resolveServerURL(*server, "connect")
	if err != nil {
		return err
	}

	client := connect.NewClient(url, sessToken, *agentID, *target)
	// Clamp --batch-size the same way the agent path does: 0 means "use
	// default" (no clamp), anything else is bounded to 1 KiB..1 MiB so a
	// typo can't trigger a 413 on the server.
	if *batchSize != 0 {
		if *batchSize < minBatchSize {
			log.Printf("connect: --batch-size %d below minimum, clamped to %d", *batchSize, minBatchSize)
			*batchSize = minBatchSize
		}
		if *batchSize > maxBatchSize {
			log.Printf("connect: --batch-size %d above maximum, clamped to %d", *batchSize, maxBatchSize)
			*batchSize = maxBatchSize
		}
	}
	client.BatchSize = *batchSize

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

// resolveServerURL resolves the server URL and session token.
// If serverFlag is set, uses it and loads the matching token.
// If serverFlag is empty, loads the first saved session.
// Proactively refreshes the token if it is near expiration.
// Returns error if no server URL can be resolved.
func resolveServerURL(serverFlag, prefix string) (url, token, consoleURL string, err error) {
	// Trim trailing slash for consistent URL handling.
	serverFlag = strings.TrimRight(serverFlag, "/")
	token, resolvedServer, consoleURL, expiresAt, sessErr := auth.LoadSession(serverFlag)

	url = serverFlag
	if url == "" {
		if sessErr != nil {
			return "", "", "", fmt.Errorf("--server is required (failed to load saved session: %w); run `ssetunnel login` first", sessErr)
		}
		if resolvedServer != "" {
			url = resolvedServer
			log.Printf("%s: using saved session for %s", prefix, url)
		} else {
			return "", "", "", fmt.Errorf("--server is required (no saved session found; run `ssetunnel login` first)")
		}
	} else {
		if sessErr != nil {
			log.Printf("%s: warning: failed to load session for %s: %v (proceeding without saved token)", prefix, url, sessErr)
		} else if token != "" {
			log.Printf("%s: using saved session for %s", prefix, url)
		}
	}

	// Warn about old-format sessions that lack refresh metadata.
	if token != "" && consoleURL == "" && expiresAt.IsZero() {
		log.Printf("%s: session lacks refresh metadata (old format); token may expire without warning — re-run `ssetunnel login` to upgrade", prefix)
	}

	// Proactive token refresh when approaching expiration.
	if token != "" && consoleURL != "" && auth.NeedsRefresh(expiresAt) {
		remaining := time.Until(expiresAt)
		if remaining < 0 {
			log.Printf("%s: session expired, attempting refresh...", prefix)
		} else {
			log.Printf("%s: session expires in %s, refreshing...", prefix, remaining.Round(time.Hour))
		}
		newToken, newExpiresAt, refreshErr := auth.RefreshSession(consoleURL, token)
		if refreshErr != nil {
			// Another process may have refreshed already — re-read from disk.
			if tok2, _, _, exp2, loadErr := auth.LoadSession(serverFlag); loadErr == nil && tok2 != "" && tok2 != token && !auth.NeedsRefresh(exp2) {
				token = tok2
				log.Printf("%s: token was refreshed by another process", prefix)
			} else if remaining < 0 {
				return "", "", "", fmt.Errorf("session expired and refresh failed: %w; run `ssetunnel login` to re-authenticate", refreshErr)
			} else {
				log.Printf("%s: warning: token refresh failed: %v (continuing with current token)", prefix, refreshErr)
			}
		} else {
			log.Printf("%s: session token refreshed (expires %s)", prefix, newExpiresAt.Format("2006-01-02"))
			// Update only token + expiry, preserving username/role/consoleURL.
			if saveErr := auth.UpdateSessionToken(url, newToken, newExpiresAt); saveErr != nil {
				log.Printf("%s: warning: failed to save refreshed session: %v", prefix, saveErr)
			}
			token = newToken
		}
	}

	return url, token, consoleURL, nil
}

// deriveTunnelURL maps a console URL on the default port 8081 to the tunnel URL
// on 8080. derived is false whenever a port swap is not the whole mapping, which
// is the case the documented proxy layout produces: console at the root and the
// tunnel under a prefix that cannot be recovered from the console URL.
//
// It parses rather than scanning for ":8081" because a substring match rewrites
// things it must not touch — http://host:80810 became http://host:80800, a
// query string or userinfo containing :8081 got rewritten, and an IPv6 address
// holding 8081 in a segment had its host mangled, all while reporting success.
func deriveTunnelURL(consoleURL string) (string, bool) {
	u, err := url.Parse(consoleURL)
	if err != nil {
		return consoleURL, false
	}
	// net/url accepts a lot that cannot be dialled: "//host:8081" parses with no
	// scheme, and "http://:8081" parses with no host. Both would come back as a
	// "successful" derivation that the agent then fails to reach.
	if u.Scheme != "http" && u.Scheme != "https" {
		return consoleURL, false
	}
	if u.Hostname() == "" || u.Port() != "8081" {
		return consoleURL, false
	}
	if strings.TrimRight(u.Path, "/") != "" {
		// The console itself sits behind a path prefix, so the tunnel is not
		// reachable by changing the port alone.
		return consoleURL, false
	}
	// Rebuild host:port from the parsed pieces. Rewriting u.Host as a string
	// would replace the first ":8081" it finds, and for an IPv6 literal that one
	// is inside the address: http://[2001:db8:8081::1]:8081 came back as
	// http://[2001:db8:8080::1]:8081 — host mangled, port untouched, no warning.
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	u.Host = host + ":8080"
	return u.String(), true
}

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

// isEmbeddedPostgres reports whether dbURL refers to an embedded or
// testcontainer-managed PostgreSQL instance (as opposed to an external
// connection string like postgres://...).
func isEmbeddedPostgres(dbURL string) bool {
	return strings.HasPrefix(dbURL, "postgres:tc:") ||
		strings.HasPrefix(dbURL, "postgres:embedded:")
}

func runProbe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	serverURL := fs.String("server", "", "tunnel server URL, e.g. http://tunnel.example.com")
	base := acceptDeprecatedBaseFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	warnDeprecatedBase("probe", *base)
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
	serverURL := fs.String("server", "http://127.0.0.1:8081", "tunnel server URL (console port)")
	tunnelServer := fs.String("tunnel-server", "", "tunnel server URL to save in session (default: --server with port 8081→8080)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Trim trailing slash to avoid double-slash in API paths.
	*serverURL = strings.TrimRight(*serverURL, "/")

	// Console API is at <server>/api/v1/...
	consoleBase := *serverURL + "/api/v1"

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
	checkResp, err := http.Post(consoleBase+"/user-login-check", "application/json", strings.NewReader(string(checkBody)))
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

	resp, err := http.Post(consoleBase+"/user-login", "application/json", strings.NewReader(string(reqBody)))
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("login failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Token     string `json:"token"`
		Username  string `json:"username"`
		Role      string `json:"role"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	// Parse expiry for session file storage.
	var expiresAt time.Time
	if result.ExpiresAt != "" {
		expiresAt, _ = time.Parse(time.RFC3339, result.ExpiresAt)
	}

	// Determine tunnel URL for session storage.
	tunnelURL := *tunnelServer
	if tunnelURL == "" {
		var derived bool
		if tunnelURL, derived = deriveTunnelURL(*serverURL); !derived {
			// Nothing was derived — the saved tunnel URL equals the console URL.
			// Agents will then dial the console and the SPA answers them 200
			// index.html, which the transport now reports instead of retrying
			// silently. Say it at the moment the value is chosen, not only in
			// the reconnect loop an hour later, but never refuse to save: a
			// console and tunnel that really do share a URL is a valid layout.
			// No reason is printed: "not derivable" covers several distinct
			// causes (no :8081 port, a path prefix on the console itself, an
			// unparseable URL), and claiming the wrong one previously sent
			// operators chasing a port that was there all along.
			fmt.Printf("Warning: the tunnel URL could not be derived from %s, so the session stores it as %s.\n", *serverURL, tunnelURL)
			fmt.Printf("If the tunnel sits behind a prefix or a different port, re-run with --tunnel-server (agents will otherwise dial %s/events).\n", tunnelURL)
		}
	}
	tunnelURL = strings.TrimRight(tunnelURL, "/")

	if err := auth.SaveSession(tunnelURL, result.Token, result.Username, result.Role, *serverURL, expiresAt); err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	fmt.Printf("Login successful! Session saved for %s (role: %s) at %s\n", result.Username, result.Role, tunnelURL)
	fmt.Println("You can now run 'ssetunnel agent' or 'ssetunnel connect' without --server.")
	return nil
}
