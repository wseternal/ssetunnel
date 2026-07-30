package consoleserver

import (
	"context"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/visdomtech/orcacommon/litespaserver"
	"github.com/wseternal/ssetunnel/frontend"
	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/consoleapi"
	"github.com/wseternal/ssetunnel/internal/metrics"
	"github.com/wseternal/ssetunnel/internal/server"
)

// NewConsoleHandler builds an HTTP handler combining the JSON management API
// under /console/api/v1/... and serving the React console SPA catch-all at
// /console/ using litespaserver.
// mc may be nil when metrics are disabled.
func NewConsoleHandler(ctx context.Context, pool *pgxpool.Pool, store *auth.Store, reg *server.Registry, mc *metrics.MetricsCollector) http.Handler {
	r := mux.NewRouter()

	// Redirect bare /console to /console/ so relative links work correctly.
	r.HandleFunc("/console", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/console/", http.StatusMovedPermanently)
	})

	// API routes — strip /console so the inner router sees /api/v1/...
	apiRouter := consoleapi.NewRouter(store, reg)
	apiRouter.SetMetrics(mc)
	r.PathPrefix("/console/api/v1/").Handler(http.StripPrefix("/console", apiRouter))

	// SPA catch-all
	spaCfg := litespaserver.Config{
		EmbeddedContent: frontend.ConsoleWebRootFs,
		CSP: litespaserver.CSPConfig{
			Disable: true,
		},
	}
	spaServer := litespaserver.NewServer(ctx, pool, spaCfg)
	r.PathPrefix("/console/").HandlerFunc(spaServer.ServeRoot).Methods("GET")

	return r
}
