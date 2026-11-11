// Package command implements the server and client command workflows shared
// by the cmd/server, cmd/cli, and cmd/ssetunnel entry points.
package command

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	orcapostgres "github.com/visdomtech/orcacommon/postgres"
	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/clientapp"
	"github.com/wseternal/ssetunnel/internal/consoleserver"
	"github.com/wseternal/ssetunnel/internal/metrics"
	"github.com/wseternal/ssetunnel/internal/probe"
	"github.com/wseternal/ssetunnel/internal/server"
	"github.com/wseternal/ssetunnel/migrations"
)

// Usage is the top-level help text for the combined ssetunnel binary.
const Usage = `usage: ssetunnel <command> [flags]

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

// ServerUsage is the top-level help text for the server binary.
const ServerUsage = `usage: server [flags]
  or: server <action> [flags]

actions: run, start, stop, restart, status, reload, uninstall

flags:
  Run 'server --help' for the full flag list.
`

// CLIUsage is the top-level help text for the CLI binary.
const CLIUsage = `usage: cli <command> [flags]

commands:
  login     authenticate and store a session for agent/connect
  agent     run the agent inside the restricted network
            service actions: run, start, stop, restart, status, reload, uninstall
  connect   run the user connect client wrapper
            service actions: run, start, stop, restart, status, uninstall
  probe     measure a server's POST path (body cap, throttling)
  module    manage agent modules (install, start, stop, restart, status, remove, list)
  version   print the build version and git revision
`

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

func RunServer(ctx context.Context, args []string) error {
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
	consolePrefix := fs.String("console-prefix", "/console", "URL prefix to strip on the console port (must match the frontend build base; empty = no stripping)")
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
		if os.Getuid() == 0 && IsEmbeddedPostgres(targetDBURL) {
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
		var consoleHandler http.Handler = consoleserver.NewConsoleHandler(ctx, store, srv.Reg, srv.MetricsCollector(), srv)
		if *consolePrefix != "" {
			// The committed frontend dist is built with base: '/console' (defined
			// in vite.config.ts), so every asset URL and fetch path carries the
			// prefix.  Strip it before routing so direct access on the console
			// port works without a reverse proxy.  In production behind a reverse
			// proxy that already strips the prefix, pass --console-prefix="".
			consoleHandler = http.StripPrefix(*consolePrefix, consoleHandler)
		}
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
			pfx := *consolePrefix
			if pfx == "" {
				pfx = "/"
			} else if !strings.HasSuffix(pfx, "/") {
				pfx += "/"
			}
			log.Printf("server: admin console http://localhost%s%s", *consoleListen, pfx)
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

func RunAgent(ctx context.Context, args []string) error {
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

	// SIGHUP: trigger configuration reload (placeholder; agent has no
	// runtime-reloadable config today — logs only).
	installSIGHUPHandler("agent", "SIGHUP received (no reloadable config)")

	dataDir, dirErr := AgentDataDir()
	if dirErr != nil {
		return dirErr
	}

	return clientapp.RunAgent(ctx, clientapp.AgentConfig{
		ServerURL:   *serverURL,
		Target:      *target,
		AgentID:     *agentID,
		BatchSize:   *batchSize,
		Concurrency: *concurrency,
		Compress:    *compress,
		NoAutoTune:  *noAutoTune,
		Verbose:     *verbose,
		Version:     BuildVersion(),
		DataDir:     dataDir,
	})
}

func RunConnect(ctx context.Context, args []string) error {
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

	return clientapp.RunConnect(ctx, clientapp.ConnectConfig{
		ServerURL: *server,
		AgentID:   *agentID,
		Target:    *target,
		Local:     *local,
		BatchSize: *batchSize,
	})
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
func ResolveServerURL(serverFlag, prefix string) (url, token, consoleURL string, err error) {
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

// DeriveTunnelURL maps a console URL on the default port 8081 to the tunnel
// URL on 8080. Returns false when the port swap is not the whole mapping.
func DeriveTunnelURL(consoleURL string) (string, bool) {
	return clientapp.DeriveTunnelURL(consoleURL)
}

func ClampAgentFlags(batchSize, concurrency int) (int, int) {
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

// IsEmbeddedPostgres reports whether dbURL refers to an embedded or
// testcontainer-managed PostgreSQL instance (as opposed to an external
// connection string like postgres://...).
func IsEmbeddedPostgres(dbURL string) bool {
	return strings.HasPrefix(dbURL, "postgres:tc:") ||
		strings.HasPrefix(dbURL, "postgres:embedded:")
}

func RunProbe(ctx context.Context, args []string) error {
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

func RunLogin(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	serverURL := fs.String("server", "http://127.0.0.1:8081", "tunnel server URL (console port)")
	tunnelServer := fs.String("tunnel-server", "", "tunnel server URL to save in session (default: --server with port 8081→8080)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Trim trailing slash to avoid double-slash in API paths.
	*serverURL = strings.TrimRight(*serverURL, "/")

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
	totpRequired, _ := clientapp.CheckTOTPRequired(*serverURL, username)
	if totpRequired {
		fmt.Print("TOTP or Recovery Code: ")
		totpCode, _ = reader.ReadString('\n')
		totpCode = strings.TrimSpace(totpCode)
	} else {
		// Non-200 status or unavailable endpoint: still allow login attempt.
		fmt.Print("TOTP or Recovery Code (press Enter to skip): ")
		totpCode, _ = reader.ReadString('\n')
		totpCode = strings.TrimSpace(totpCode)
	}

	result, err := clientapp.Login(clientapp.LoginParams{
		ConsoleURL: *serverURL,
		TunnelURL:  *tunnelServer,
		Username:   username,
		Password:   password,
		TOTPCode:   totpCode,
	})
	if err != nil {
		return err
	}

	// Warn when the tunnel URL could not be derived from the console URL.
	if *tunnelServer == "" {
		if _, derived := clientapp.DeriveTunnelURL(*serverURL); !derived {
			fmt.Printf("Warning: the tunnel URL could not be derived from %s, so the session stores it as %s.\n", *serverURL, result.TunnelURL)
			fmt.Printf("If the tunnel sits behind a prefix or a different port, re-run with --tunnel-server (agents will otherwise dial %s/events).\n", result.TunnelURL)
		}
	}

	fmt.Printf("Login successful! Session saved for %s (role: %s) at %s\n", result.Username, result.Role, result.TunnelURL)
	fmt.Println("You can now run 'ssetunnel agent' or 'ssetunnel connect' without --server.")
	return nil
}
