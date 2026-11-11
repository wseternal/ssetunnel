package consoleserver

import (
	"context"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"github.com/visdomtech/orcacommon/litespaserver"
	"github.com/wseternal/ssetunnel/frontend/console"
	"github.com/wseternal/ssetunnel/internal/auth"
	"github.com/wseternal/ssetunnel/internal/consoleapi"
	"github.com/wseternal/ssetunnel/internal/metrics"
	"github.com/wseternal/ssetunnel/internal/moduleui"
	"github.com/wseternal/ssetunnel/internal/server"
)

// consoleHeaders returns next with this server's security headers and a default
// Cache-Control already declared. The set matches what litespaserver.setBaseHeaders
// puts on responses it writes, but it has to be applied above the router because
// mux answers 404, 405 and path-cleanup 301 itself, and litespaserver never sees
// those at all.
//
// It mutates the header map and then calls next with the ORIGINAL
// ResponseWriter: several handlers here stream, and server.shellCreate asserts
// w.(http.Flusher) directly, so wrapping the writer in a struct — even one that
// implements Unwrap — turns the cloud shell into a 500 "streaming unsupported"
// and, if it only loses SetWriteDeadline, silently disables dead-client
// detection.
//
// Handlers overwrite these with Set, so what is declared here is only a default:
// the asset handler still wins with immutable, the SSE writers with no-transform,
// litespaserver with its own no-store. The module proxy is the exception — it
// the module proxy deletes each key before copying it (see copyProxiedHeader).
func consoleHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "origin-when-cross-origin")
		// Unstorable by default: the API responses are user-scoped, and a cached
		// 301 or 404 would pin a bad prefix.
		h.Set("Cache-Control", "no-store")
		if req.URL.Path == "*" {
			// OPTIONS * arrives with no path to route. mux would treat "*" as a
			// path needing cleanup and answer a 301 to /%2A, which the SPA
			// catch-all then serves as index.html, so it is refused here — above
			// routing, but inside the header contract.
			h.Set("Allow", "OPTIONS, GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, req)
	})
}

// ApplyServerDefaults sets the http.Server options the console's header
// contract depends on. net/http answers OPTIONS * itself, before any handler
// runs, so without this the one response class the router cannot reach would
// ship without headers — and nothing in NewConsoleHandler's own code would say
// so. Callers that build the server must invoke this on it.
func ApplyServerDefaults(s *http.Server) {
	s.DisableGeneralOptionsHandler = true
}

// missWindow lets one class of console miss write at most one journal record a
// minute, so an unauthenticated scanner cannot produce a record per request.
//
// Two details are load-bearing. Each miss site owns its own window: with a
// single shared slot, a scanner hammering /assets/ at one request per second
// consumed the window and erased the genuine "every asset 404s, console blank"
// line — measured over an hour, 60 scanner lines and zero real ones. And the
// the clock is monotonic, so an NTP step or a wall-clock jump backwards cannot
// silence logging the way a Unix-seconds comparison could — measured: a 3 s
// backward step suppressed the next call, a larger one stopped logging entirely.
//
// Only the goroutine that wins the compare-and-swap moves the deadline. Using
// Swap would advance it on suppressed calls too, so requests arriving slightly
// faster than a minute apart would log about once an hour instead of once a
// minute — measured: 100 misses at a 30 s cadence produced zero lines.
type missWindow struct {
	next atomic.Int64
}

func (mw *missWindow) log(format string, args ...any) {
	// Elapsed monotonic nanoseconds since process start. sync/atomic has no Time
	// type in this Go version, and Unix seconds are steerable by the clock.
	now := int64(missClock())
	if last := mw.next.Load(); now < last {
		return
	} else if !mw.next.CompareAndSwap(last, now+int64(time.Minute)) {
		return
	}
	log.Printf(format, args...)
}

// missClock is a variable so tests can drive the monotonic clock; it is the
// elapsed nanoseconds since process start, never a wall-clock reading.
var missClock = func() time.Duration { return time.Since(missStart) }

var missStart = time.Now()

var (
	missAssetReject missWindow
	missAssetStat   missWindow
	missRoute       missWindow
)

// CopyProxiedHeader transfers one allowlisted response header from a proxied
// module response onto a console reply. It deletes the key first because multi-
// valued headers must keep every value, which means appending — and appending
// onto the defaults the router pre-declares would ship two values that disagree.
//
// Cache-Control must not be passed through here: module output is user-scoped,
// so a module's "public, max-age=…" would make it cacheable across users. The
// caller's allowlist leaves it out; this asserts that it stayed out.
func CopyProxiedHeader(dst http.Header, key string, values []string) {
	if strings.EqualFold(key, "Cache-Control") {
		return
	}
	dst.Del(key)
	for _, v := range values {
		dst.Add(key, v)
	}
}

// serveConsoleAsset serves one content-hashed Vite asset from the embedded FS.
// It bypasses litespaserver on purpose: that library puts no-store on every
// response it writes, which defeats the point of a hashed filename, so the
// security headers it also applies are supplied by the consoleHeaders wrapper.
func serveConsoleAsset(w http.ResponseWriter, req *http.Request) {
	// ConsoleWebRootFs is fs.Sub(...), whose Open rejects any path that fails
	// fs.ValidPath — and ValidPath forbids a leading slash, so the URL path has
	// to be trimmed before it is used as an FS name.
	name := strings.TrimPrefix(path.Clean(req.URL.Path), "/")
	// The route matched on the /assets/ prefix, so keep the resolved name inside
	// that directory: path.Clean runs before the ValidPath gate, which means a
	// URL like /assets/../index.html would otherwise name a file at the FS root.
	// Today mux's 301 cleanup and ServeFileFS's own dot-dot check stop that, but
	// neither is asserted here, so the containment is stated explicitly.
	if !strings.HasPrefix(name, "assets/") || !fs.ValidPath(name) {
		missAssetReject.log("console: rejected asset request %q", req.URL.Path)
		http.NotFound(w, req)
		return
	}
	info, err := fs.Stat(console.ConsoleWebRootFs, name)
	if err != nil {
		// err.Error() embeds the request path verbatim, so it must be quoted:
		// an unescaped newline or escape sequence in the URL would otherwise
		// forge extra journal records from a single unauthenticated request.
		// Misses are still recorded — silently dropping them removes the only
		// server-side trace of the failure this package already shipped once
		// (a stale or mis-hashed bundle 404ing every asset), and the documented
		// Caddy recipe writes no access log, so nothing else would show it.
		missAssetStat.log("console: stat asset %q failed: %q", name, err.Error())
		http.NotFound(w, req)
		return
	}
	if info.IsDir() {
		missAssetStat.log("console: asset request %q resolves to a directory", name)
		http.NotFound(w, req)
		return
	}
	ct := mime.TypeByExtension(path.Ext(name))
	hashed := ct != ""
	if ct == "" {
		// Fall back to a neutral type rather than 404: the extension may be
		// missing only because this image has no /etc/mime.types, and a dropped
		// font is worse than an opaque content type. A declared type also keeps
		// ServeFileFS from sniffing, which is what nosniff asserts.
		ct = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Vary", "Accept-Encoding")
	h.Set("Content-Type", ct)
	if hashed {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// A type we had to guess is not something to pin for a year: with
		// nosniff the browser will refuse to run it, and immutable would keep
		// that broken answer cached through the fix. The ETag below makes
		// revalidation a header-only 304, so this costs nothing on reload.
		h.Set("Cache-Control", "private, max-age=0, must-revalidate")
	}
	// The filename is already a content hash, so it doubles as a validator:
	// embed.FS reports a zero ModTime, which leaves ServeFileFS unable to emit
	// Last-Modified and makes 304 impossible without an explicit ETag.
	w.Header().Set("ETag", strconv.Quote(path.Base(name)))
	http.ServeFileFS(w, req, console.ConsoleWebRootFs, name)
}

// NewConsoleHandler builds an HTTP handler combining the JSON management API
// under /api/v1/... and serving the React console SPA catch-all at
// / using litespaserver.
// mc may be nil when metrics are disabled.
// srv is the tunnel server; its handler is used for cloud shell proxying.
//
// There is no database parameter: with EmbeddedContent set, litespaserver locks
// its version to "embedded" and never queries. That coupling is load-bearing —
// if frontend/console/dist ever loses index.html, resolveEmbedded drops to the
// CDN/DB provider and a nil pool would be dereferenced. The "SPA shell is served
// at the root" subtest fails first if that happens.
func NewConsoleHandler(ctx context.Context, store *auth.Store, reg *server.Registry, mc *metrics.MetricsCollector, srv *server.Server) http.Handler {
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

	// Content-hashed static assets, with immutable caching. Registered before
	// the SPA catch-all so /assets/* never falls through to index.html.
	r.PathPrefix("/assets/").HandlerFunc(serveConsoleAsset).Methods("GET", "HEAD")

	// Paths that look like an API call or an asset must fail loudly, never
	// return the SPA shell. litespaserver's own JSON guard keys on an
	// `Accept: application/json` header that the console's fetch calls do not
	// send, so without this an unmatched /api/... or bare /assets request
	// answers 200 text/html — indistinguishable from success in the client.
	notFound := func(w http.ResponseWriter, req *http.Request) {
		missRoute.log("console: no route for %s %q", req.Method, req.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
	}
	r.PathPrefix("/api/").HandlerFunc(notFound).Methods("GET", "HEAD")
	// PathPrefix("/api/") does not match the bare forms, and both would otherwise
	// fall through to the SPA catch-all and answer 200 index.html.
	r.Path("/api").HandlerFunc(notFound).Methods("GET", "HEAD")
	r.Path("/assets").HandlerFunc(notFound).Methods("GET", "HEAD")

	// SPA catch-all — must be the LAST registered route. All other GET
	// handlers (shell, remoteapp, modules, assets, not-found guards) MUST be
	// registered before this line, otherwise they will silently serve
	// index.html.
	spaCfg := litespaserver.Config{
		EmbeddedContent: console.ConsoleWebRootFs,
		// StaticPaths is deliberately unset: /assets/** is served by the
		// dedicated handler above so hashed files can be cached immutably, and
		// litespaserver would force no-store on them.
		// Pin the version so litespaserver takes the static provider even if
		// resolveEmbedded rejects the FS (a console/dist with no index.html),
		// which would otherwise fall through to the database path — and with no
		// pool that panics during NewConsoleHandler, killing the whole server.
		CDNVersion: "embedded",
		CSP: litespaserver.CSPConfig{
			Disable: true,
		},
	}
	spaServer := litespaserver.NewServer(ctx, nil, spaCfg)
	r.PathPrefix("/").HandlerFunc(spaServer.ServeRoot).Methods("GET", "HEAD")

	// Recorded because the two most common console failures — a 404 on every
	// asset and an API call answered with HTML — both come from the embedded
	// bundle's build-time base disagreeing with the prefix the reverse proxy
	// actually strips. This is the line to compare against the proxy config.
	if _, err := fs.Stat(console.ConsoleWebRootFs, "index.html"); err != nil {
		// Without index.html litespaserver disables embedded mode and answers a
		// 200 error page instead of the SPA, so the console is silently dead
		// until someone reads this. It cannot happen from a normal build —
		// task fe:dist-drift rejects a bundle that does not match src/.
		log.Printf("console: ERROR embedded console/dist has no index.html (%v); the SPA will not load", err)
	}

	log.Printf("console: serving SPA from embedded console/dist at /, API under \"/api/v1/\", assets under \"/assets/\"")

	// Wrapped rather than registered as mux middleware: mux applies r.Use only to
	// requests that match a route, so its own 404/405 and path-cleanup 301
	// responses would otherwise go out with no security headers at all.
	return consoleHeaders(r)
}
