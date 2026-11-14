// Package command implements the server-side command workflow and shared
// infrastructure (service management, version, flag helpers) used by the
// cmd/server, cmd/cli, and cmd/ssetunnel entry points. Client-side commands
// (agent, connect, login, probe, module, ui) live in internal/clicommand
// to avoid transitively importing client packages into cmd/server.
package command

import (
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
	"github.com/wseternal/ssetunnel/internal/consoleserver"
	"github.com/wseternal/ssetunnel/internal/metrics"
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
  ui        serve the client UI on a secured loopback URL
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
  ui        serve the client UI on a secured loopback URL
  version   print the build version and git revision
`

// AcceptDeprecatedBaseFlag keeps --base parseable after route prefixing moved
// to the reverse proxy. A package upgrade does not rewrite an installed unit
// file, so a service started with --base keeps passing it on every restart;
// rejecting the flag would abort the daemon into a restart loop that a bare
// `start` then re-poisons from the saved args file. The value is read back only
// so the caller can say it no longer does anything.
func AcceptDeprecatedBaseFlag(fs *flag.FlagSet) *string {
	return fs.String("base", "", "DEPRECATED: ignored — path prefixing is delegated to the reverse proxy")
}

func WarnDeprecatedBase(prefix, base string) {
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
	base := AcceptDeprecatedBaseFlag(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}
	WarnDeprecatedBase("server", *base)

	// Deprecation warning for removed global TOTP secret.
	if os.Getenv("SSETUNNEL_TOTP_SECRET") != "" {
		log.Println("server: WARNING: SSETUNNEL_TOTP_SECRET is deprecated; per-user TOTP is now used. Set up TOTP via the console.")
	}

	// SIGHUP: trigger configuration reload (placeholder).
	InstallSIGHUPHandler("server", "SIGHUP received, reloading configuration...")
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
		if pool != nil {
			pool.Close()
		}
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

// IsEmbeddedPostgres reports whether dbURL refers to an embedded or
// testcontainer-managed PostgreSQL instance (as opposed to an external
// connection string like postgres://...).
func IsEmbeddedPostgres(dbURL string) bool {
	return strings.HasPrefix(dbURL, "postgres:tc:") ||
		strings.HasPrefix(dbURL, "postgres:embedded:")
}
