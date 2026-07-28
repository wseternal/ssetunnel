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
	"github.com/wseternal/ssetunnel/internal/server"
)

// NewConsoleHandler builds an HTTP handler combining the JSON management API
// under /api/v1/... and serving the React console SPA catch-all using litespaserver.
func NewConsoleHandler(ctx context.Context, pool *pgxpool.Pool, store *auth.Store, reg *server.Registry) http.Handler {
	r := mux.NewRouter()

	// API routes
	apiHandler := consoleapi.NewRouter(store, reg)
	r.PathPrefix("/api/v1/").Handler(apiHandler)

	// SPA catch-all
	spaCfg := litespaserver.Config{
		EmbeddedContent: frontend.ConsoleWebRootFs,
		CSP: litespaserver.CSPConfig{
			ScriptSrcs:   litespaserver.ScriptSrcAll,
			StyleSrcs:    litespaserver.StyleSrcAll,
			ConnectSrcs:  litespaserver.ConnectSrcAll,
			FontSrcs:     litespaserver.FontSrcAll,
			ManifestSrcs: litespaserver.ManifestSrcAll,
		},
	}
	spaServer := litespaserver.NewServer(ctx, pool, spaCfg)
	r.PathPrefix("/").HandlerFunc(spaServer.ServeRoot).Methods("GET")

	return r
}
