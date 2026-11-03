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
	"github.com/wseternal/ssetunnel/internal/moduleui"
	"github.com/wseternal/ssetunnel/internal/server"
)

// NewConsoleHandler builds an HTTP handler combining the JSON management API
// under /api/v1/... and serving the React console SPA catch-all at
// / using litespaserver.
// mc may be nil when metrics are disabled.
// srv is the tunnel server; its handler is used for cloud shell proxying.
func NewConsoleHandler(ctx context.Context, pool *pgxpool.Pool, store *auth.Store, reg *server.Registry, mc *metrics.MetricsCollector, srv *server.Server) http.Handler {
	r := mux.NewRouter()

	// Cloud shell: proxy to the tunnel handler's /connect and /connect-up
	// endpoints with forced target=__shell__. Auth via user session middleware.
	// IMPORTANT: These routes MUST be registered before the /api/v1/
	// PathPrefix catch-all below, otherwise gorilla/mux matches the prefix
	// first and the shell connect requests never reach these handlers.
	if srv != nil {
		th := srv.TunnelHandler()
		userAuth := server.UserSessionMiddleware(store)
		r.Handle("/api/v1/shell/connect", userAuth(th.ShellConnectHandler())).Methods("GET")
		r.Handle("/api/v1/shell/connect-up", userAuth(th.ShellConnectUpHandler())).Methods("POST")
		r.Handle("/api/v1/shell/resize", userAuth(th.ShellConnectResizeHandler())).Methods("POST")
		r.Handle("/api/v1/shell/sessions", userAuth(th.ShellSessionListHandler())).Methods("GET")
		r.Handle("/api/v1/shell/sessions/delete", userAuth(th.ShellSessionDeleteHandler())).Methods("POST", "DELETE")

		// Remote desktop: proxy to the tunnel handler's /connect and
		// /connect-up endpoints with forced target=__remote_app__.
		r.Handle("/api/v1/remoteapp/connect", userAuth(th.RemoteAppConnectHandler())).Methods("GET")
		r.Handle("/api/v1/remoteapp/connect-up", userAuth(th.RemoteAppConnectUpHandler())).Methods("POST")

		// Module routes: discovery and invocation APIs.
		modRoutes := newModuleRoutes(srv)
		r.Handle("/api/v1/modules", userAuth(http.HandlerFunc(modRoutes.handleListModules))).Methods("GET")
		r.Handle("/api/v1/modules/openapi/{agent-id}/{module-id}", userAuth(http.HandlerFunc(modRoutes.handleGetOpenAPI))).Methods("GET")
		r.Handle("/api/v1/modules/invoke/{agent-id}/{module-id}/{path:.*}", userAuth(http.HandlerFunc(modRoutes.handleInvoke)))
		r.Handle("/api/v1/modules/invoke/{agent-id}/{module-id}", userAuth(http.HandlerFunc(modRoutes.handleInvoke)))
		r.Handle("/api/v1/modules/{agent-id}/{module-id}", userAuth(http.HandlerFunc(modRoutes.handleGetModule))).Methods("GET")

		// Module UI routes: asset serving for federation.
		uiStore := moduleui.NewStore(moduleui.MaxCacheSize)
		uiRoutes := newModuleUIRoutes(srv, uiStore)
		r.Handle("/api/v1/modules/ui/{agent-id}/{module-id}", userAuth(http.HandlerFunc(uiRoutes.handleListModuleUI))).Methods("GET")
		r.Handle("/api/v1/modules/ui/consumer/{package-digest}/{path:.*}", userAuth(http.HandlerFunc(uiRoutes.handleGetConsumerAsset))).Methods("GET")
		r.Handle("/api/v1/modules/ui/provider/{agent-id}/{module-id}/{path:.*}", userAuth(http.HandlerFunc(uiRoutes.handleGetProviderAsset))).Methods("GET")
	}

	// API routes catch-all.
	apiRouter := consoleapi.NewRouter(store, reg)
	apiRouter.SetMetrics(mc)
	r.PathPrefix("/api/v1/").Handler(apiRouter)

	// SPA catch-all
	spaCfg := litespaserver.Config{
		EmbeddedContent: frontend.ConsoleWebRootFs,
		StaticPaths:     []string{"/assets/**"},
		CSP: litespaserver.CSPConfig{
			Disable: true,
		},
	}
	spaServer := litespaserver.NewServer(ctx, pool, spaCfg)
	r.PathPrefix("/").HandlerFunc(spaServer.ServeRoot).Methods("GET")

	return r
}
