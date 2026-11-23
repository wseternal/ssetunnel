package agent

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	gmux "github.com/gorilla/mux"
	"github.com/wseternal/ssetunnel/internal/agentmod"
	"github.com/wseternal/ssetunnel/internal/moduleapi"
	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"github.com/wseternal/ssetunnel/sdk"
)

const (
	// moduleAPIAddr is the loopback address the direct API binds. The port is
	// chosen by the kernel and published in the agent status file, so two
	// agents on one machine never collide.
	moduleAPIAddr = "127.0.0.1:0"

	// moduleAPIEpochPrefix marks the epoch as process-local rather than the
	// tunnel session ID the server sees. A consumer that mixes the two would
	// compare a session ID with a token that never matches.
	moduleAPIEpochPrefix = "local-"
)

// moduleAPIServer is the agent's direct module API: the same module surface the
// console serves, addressed without the tunnel.
//
// It is bound to loopback and its guards assume that. The API holds no
// credential: the boundary is the network namespace plus the browser protections
// below, and its callers are the SDK, curl, and the agent's own client UI.
type moduleAPIServer struct {
	agentID  string
	epoch    string
	invoker  *ModuleInvoker
	manager  *ModuleManager
	registry *agentmod.Registry

	// contracts caches the parsed OpenAPI document and its form models by
	// module ID. The entry is replaced when the module revision changes; a
	// revision is the module's contract identity.
	contracts sync.Map // map[string]*moduleContract

	ln      net.Listener
	srv     *http.Server
	addr    string
	baseURL string
}

// moduleContract is the immutable-by-revision parsed contract for one module.
// The operation models are built at most once per contract.
type moduleContract struct {
	revision  string
	validated *agentmod.ValidatedOpenAPI

	modelsOnce     sync.Once
	modelsTitle    string
	models         []agentmod.APIOperation
	modelsBuildErr error
}

func (c *moduleContract) operationModels(openAPIYAML []byte) (string, []agentmod.APIOperation, error) {
	c.modelsOnce.Do(func() {
		c.modelsTitle, c.models, c.modelsBuildErr = agentmod.BuildOperationModels(openAPIYAML)
	})
	return c.modelsTitle, c.models, c.modelsBuildErr
}

// contractFor returns the cached contract for the module's current revision,
// parsing the OpenAPI document only when the revision changed. The map holds
// entries for all modules seen by this API process; invalidation is by revision
// comparison, not time.
func (s *moduleAPIServer) contractFor(mod *moduleproto.SnapshotModuleEntry) (*moduleContract, error) {
	if mod == nil {
		return nil, errors.New("nil module entry")
	}
	if cached, ok := s.contracts.Load(mod.ModuleID); ok {
		if contract, ok := cached.(*moduleContract); ok && contract.revision == mod.Revision {
			return contract, nil
		}
	}
	validated, err := agentmod.ValidateOpenAPI(mod.OpenAPIYAML)
	if err != nil {
		return nil, err
	}
	contract := &moduleContract{revision: mod.Revision, validated: validated}
	s.contracts.Store(mod.ModuleID, contract)
	return contract, nil
}

// newModuleAPIServer builds the API for an agent that has modules initialized.
func newModuleAPIServer(a *Agent) *moduleAPIServer {
	token := strings.TrimPrefix(moduleapi.NewCorrelationID(), "corr-")
	return &moduleAPIServer{
		agentID:  a.AgentID,
		epoch:    moduleAPIEpochPrefix + token,
		invoker:  a.moduleInvoker,
		manager:  a.moduleManager,
		registry: a.modRegistry,
	}
}

// start binds the loopback listener and serves until Close.
func (s *moduleAPIServer) start() error {
	ln, err := net.Listen("tcp", moduleAPIAddr)
	if err != nil {
		return fmt.Errorf("listen module api: %w", err)
	}
	s.ln = ln
	s.addr = ln.Addr().String()
	s.baseURL = "http://" + s.addr

	router := s.routes()
	s.srv = &http.Server{
		Handler: s.localBoundary(router),
		// The invocation deadline (30 s on the module connection) bounds the
		// exchanges this server handles. Read/write timeouts are left unset so
		// a slow module response is not truncated mid-stream; a stalled
		// connection is reclaimed by the deadline and by idle timeout.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("agent: module api: serve: %v", err)
		}
	}()
	log.Printf("agent: module API listening on %s", s.baseURL)
	return nil
}

// Close shuts the API down. It is idempotent.
func (s *moduleAPIServer) Close() {
	if s == nil || s.srv == nil {
		return
	}
	_ = s.srv.Close()
}

// URL returns the published base URL, or "" when the API never started.
func (s *moduleAPIServer) URL() string {
	if s == nil {
		return ""
	}
	return s.baseURL
}

// routes registers the module surface in the same shape the console serves.
// Registration order matters: the generic two-segment detail route must be
// last, or it would swallow the more specific ones.
func (s *moduleAPIServer) routes() http.Handler {
	// The access rule is installed above this router (see start) so it also
	// covers the router's own 404/405 answers; a middleware registered with
	// Use would only run for matched routes.
	r := gmux.NewRouter()

	r.HandleFunc("/api/v1/modules", s.handleListModules).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/modules/openapi/{agent-id}/{module-id}", s.handleGetOpenAPI).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/modules/ui/consumer/{package-digest}/{path:.*}", s.handleGetConsumerAsset).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/modules/ui/provider/{agent-id}/{module-id}/{path:.*}", s.handleGetProviderAsset).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/modules/ui/{agent-id}/{module-id}", s.handleListModuleUI).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/modules/{agent-id}/{module-id}/operations", s.handleGetOperationModels).Methods(http.MethodGet)
	r.PathPrefix("/api/v1/modules/invoke/{agent-id}/{module-id}").HandlerFunc(s.handleInvoke)
	r.HandleFunc("/api/v1/modules/{agent-id}/{module-id}", s.handleGetModule).Methods(http.MethodGet)

	return r
}

// localBoundary is the API's access rule. It refuses anything that is not a
// loopback client addressing this listener directly, and refuses browser
// requests that reveal a foreign origin — the DNS-rebinding shape a loopback
// bind alone does not stop.
func (s *moduleAPIServer) localBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackPeer(r.RemoteAddr) {
			http.Error(w, "loopback clients only", http.StatusForbidden)
			return
		}
		// The Host header must name this listener. A rebinding hostname
		// resolves to 127.0.0.1 but arrives with its own name here.
		if !strings.EqualFold(r.Host, s.addr) {
			http.Error(w, "host mismatch", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !strings.EqualFold(origin, "http://"+s.addr) {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		// A browser cannot forge these headers; a page at another origin
		// cannot send a same-origin value. "none" is a direct navigation or a
		// non-browser client.
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && !strings.EqualFold(site, "same-origin") && !strings.EqualFold(site, "none") {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		// CORS preflights are answered only by surfaces that opted into CORS.
		// This one has not: a preflight here means a browser at another
		// origin is probing, so it is refused before any module is touched.
		if moduleapi.IsPreflight(r) {
			http.Error(w, "cross-origin preflight refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackPeer reports whether the remote address is a loopback IP.
func isLoopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// acceptsAgent reports whether the API answers for this agent ID. The
// configured ID is required; "local" is accepted only when no ID was configured.
func (s *moduleAPIServer) acceptsAgent(agentID string) bool {
	if agentID == "" {
		return true
	}
	if s.agentID == "" {
		return strings.EqualFold(agentID, "local")
	}
	return strings.EqualFold(agentID, s.agentID)
}

// snapshot returns the current catalog. It is rebuilt per request from the
// registry and live IPC health, so a module that registers after startup
// appears without waiting for a tunnel connect.
func (s *moduleAPIServer) snapshot() *CatalogSnapshot {
	if s.manager == nil {
		return &CatalogSnapshot{Epoch: s.epoch}
	}
	return s.manager.LocalSnapshot(s.epoch)
}

// module returns one enabled module entry from a fresh snapshot.
func (s *moduleAPIServer) module(moduleID string) (*moduleproto.SnapshotModuleEntry, bool) {
	snap := s.snapshot()
	for i := range snap.Modules {
		if snap.Modules[i].ModuleID == moduleID {
			return &snap.Modules[i], true
		}
	}
	return nil, false
}

// staleEpoch reports a caller that quotes an epoch other than this process's.
func (s *moduleAPIServer) staleEpoch(r *http.Request) bool {
	epoch := r.URL.Query().Get("epoch")
	return epoch != "" && epoch != s.epoch
}

func (s *moduleAPIServer) writeStaleEpoch(w http.ResponseWriter) {
	w.Header().Set("X-Catalog-Epoch", s.epoch)
	http.Error(w, "stale catalog epoch", http.StatusConflict)
}

// moduleSummary is the discovery row, matching the console's JSON shape.
type moduleSummary struct {
	ModuleID       string `json:"module_id"`
	ServiceName    string `json:"service_name"`
	ServiceVersion string `json:"service_version"`
	Revision       string `json:"revision,omitempty"`
	Health         string `json:"health"`
	HasUI          bool   `json:"has_ui"`
}

type moduleDetail struct {
	ModuleID       string `json:"module_id"`
	ServiceName    string `json:"service_name"`
	ServiceVersion string `json:"service_version"`
	Revision       string `json:"revision,omitempty"`
	Health         string `json:"health"`
	HealthMessage  string `json:"health_message,omitempty"`
	CheckedAt      string `json:"checked_at"`
	OperationCount int    `json:"operation_count"`
	HasUI          bool   `json:"has_ui"`
}

type moduleOperationsResponse struct {
	ModuleID   string                  `json:"module_id"`
	Revision   string                  `json:"revision,omitempty"`
	Title      string                  `json:"title"`
	Operations []agentmod.APIOperation `json:"operations"`
}

// handleListModules returns the module catalog.
// GET /api/v1/modules?agent=<agent-id>
func (s *moduleAPIServer) handleListModules(w http.ResponseWriter, r *http.Request) {
	if s.staleEpoch(r) {
		s.writeStaleEpoch(w)
		return
	}
	if !s.acceptsAgent(r.URL.Query().Get("agent")) {
		http.Error(w, "unknown agent id", http.StatusNotFound)
		return
	}

	snap := s.snapshot()
	result := make([]moduleSummary, 0, len(snap.Modules))
	for _, mod := range snap.Modules {
		result = append(result, moduleSummary{
			ModuleID:       mod.ModuleID,
			ServiceName:    mod.ServiceName,
			ServiceVersion: mod.ServiceVersion,
			Revision:       mod.Revision,
			Health:         mod.Health.Status,
			HasUI:          len(mod.UI) > 0,
		})
	}
	w.Header().Set("X-Catalog-Epoch", s.epoch)
	moduleapi.WriteJSON(w, http.StatusOK, result)
}

// handleGetModule returns module metadata.
func (s *moduleAPIServer) handleGetModule(w http.ResponseWriter, r *http.Request) {
	vars := gmux.Vars(r)
	if !s.acceptsAgent(vars["agent-id"]) {
		http.Error(w, "unknown agent id", http.StatusNotFound)
		return
	}
	if s.staleEpoch(r) {
		s.writeStaleEpoch(w)
		return
	}

	mod, ok := s.module(vars["module-id"])
	if !ok {
		http.Error(w, "module not found", http.StatusNotFound)
		return
	}

	opCount := 0
	if contract, err := s.contractFor(mod); err == nil {
		opCount = len(contract.validated.Operations)
	} else {
		log.Printf("agent: module api: validate OpenAPI for %s: %v", mod.ModuleID, err)
	}

	checkedAt := ""
	if mod.Health.CheckedAt != 0 {
		checkedAt = time.UnixMilli(mod.Health.CheckedAt).Format(time.RFC3339)
	}

	w.Header().Set("X-Catalog-Epoch", s.epoch)
	moduleapi.WriteJSON(w, http.StatusOK, moduleDetail{
		ModuleID:       mod.ModuleID,
		ServiceName:    mod.ServiceName,
		ServiceVersion: mod.ServiceVersion,
		Revision:       mod.Revision,
		Health:         mod.Health.Status,
		HealthMessage:  mod.Health.Message,
		CheckedAt:      checkedAt,
		OperationCount: opCount,
		HasUI:          len(mod.UI) > 0,
	})
}

// handleGetOpenAPI returns the module's OpenAPI document.
func (s *moduleAPIServer) handleGetOpenAPI(w http.ResponseWriter, r *http.Request) {
	vars := gmux.Vars(r)
	if !s.acceptsAgent(vars["agent-id"]) {
		http.Error(w, "unknown agent id", http.StatusNotFound)
		return
	}
	if s.staleEpoch(r) {
		s.writeStaleEpoch(w)
		return
	}
	mod, ok := s.module(vars["module-id"])
	if !ok {
		http.Error(w, "module not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("X-Module-Revision", mod.Revision)
	w.Write(mod.OpenAPIYAML)
}

// handleGetOperationModels returns the workbench model for a module, parsed on
// demand from the catalog's OpenAPI document (bounded at 1 MiB).
func (s *moduleAPIServer) handleGetOperationModels(w http.ResponseWriter, r *http.Request) {
	vars := gmux.Vars(r)
	if !s.acceptsAgent(vars["agent-id"]) {
		http.Error(w, "unknown agent id", http.StatusNotFound)
		return
	}
	if s.staleEpoch(r) {
		s.writeStaleEpoch(w)
		return
	}
	mod, ok := s.module(vars["module-id"])
	if !ok {
		http.Error(w, "module not found", http.StatusNotFound)
		return
	}
	contract, err := s.contractFor(mod)
	if err != nil {
		log.Printf("agent: module api: validate OpenAPI for %s: %v", mod.ModuleID, err)
		http.Error(w, "module OpenAPI document cannot be rendered", http.StatusInternalServerError)
		return
	}
	title, operations, err := contract.operationModels(mod.OpenAPIYAML)
	if err != nil {
		log.Printf("agent: module api: build operation models for %s: %v", mod.ModuleID, err)
		http.Error(w, "module OpenAPI document cannot be rendered", http.StatusInternalServerError)
		return
	}
	w.Header().Set("X-Module-Revision", mod.Revision)
	moduleapi.WriteJSON(w, http.StatusOK, moduleOperationsResponse{
		ModuleID:   mod.ModuleID,
		Revision:   mod.Revision,
		Title:      title,
		Operations: operations,
	})
}

// handleInvoke performs one module operation directly, without a tunnel stream.
func (s *moduleAPIServer) handleInvoke(w http.ResponseWriter, r *http.Request) {
	vars := gmux.Vars(r)
	agentID := vars["agent-id"]
	moduleID := vars["module-id"]
	if !s.acceptsAgent(agentID) {
		http.Error(w, "unknown agent id", http.StatusNotFound)
		return
	}

	method, _ := moduleapi.ResolveMethod(r.Method, r.Header.Get(moduleapi.MethodOverrideHeader))

	// Take the operation path from the escaped URL so an encoded character in
	// a path parameter stays one segment.
	operationPath := "/" + vars["path"]
	if vars["path"] == "" {
		operationPath = "/"
	}
	if escaped, ok := moduleapi.TrailAfterSegments(r.URL.EscapedPath(), "/api/v1/modules/invoke/", 2); ok {
		operationPath = escaped
	}

	mod, ok := s.module(moduleID)
	if !ok {
		http.Error(w, "module not found", http.StatusNotFound)
		return
	}
	contract, err := s.contractFor(mod)
	if err != nil {
		log.Printf("agent: module api: validate OpenAPI for %s: %v", moduleID, err)
		http.Error(w, "module contract unavailable", http.StatusServiceUnavailable)
		return
	}
	op, ok := contract.validated.MatchOperation(method, operationPath)
	if !ok {
		http.Error(w, fmt.Sprintf("operation not found: %s %s", method, operationPath), http.StatusNotFound)
		return
	}

	invReq, err := moduleapi.BuildRequest(r.Context(), r, method, operationPath)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	resp, err := s.invoker.InvokeLocal(r.Context(), moduleID, mod.Revision, invReq)
	if err != nil {
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, ErrModuleNotFound):
			status = http.StatusNotFound
		case errors.Is(err, ErrModuleRevisionMismatch):
			status = http.StatusConflict
		case errors.Is(err, ErrModuleCapacity):
			status = http.StatusTooManyRequests
		case errors.Is(err, ErrModuleInvokerClosed):
			status = http.StatusServiceUnavailable
		}
		log.Printf("agent: module api: invoke %s %s on %s: %v", method, operationPath, moduleID, err)
		http.Error(w, fmt.Sprintf("invocation failed: %v", err), status)
		return
	}
	defer resp.Body.Close()

	moduleapi.CopyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set(moduleapi.CorrelationHeader, invReq.Header.Get(moduleapi.CorrelationHeader))
	w.WriteHeader(resp.StatusCode)
	if err := moduleapi.CopyStream(w, resp.Body); err != nil {
		log.Printf("agent: module api: copy response for %s (op=%s): %v", moduleID, op.OperationID, err)
	}
}

// handleListModuleUI returns the module's UI descriptors.
func (s *moduleAPIServer) handleListModuleUI(w http.ResponseWriter, r *http.Request) {
	vars := gmux.Vars(r)
	if !s.acceptsAgent(vars["agent-id"]) {
		http.Error(w, "unknown agent id", http.StatusNotFound)
		return
	}
	mod, ok := s.module(vars["module-id"])
	if !ok {
		http.Error(w, "module not found", http.StatusNotFound)
		return
	}
	if len(mod.UI) == 0 {
		http.Error(w, "module has no UI", http.StatusNotFound)
		return
	}
	moduleapi.WriteJSON(w, http.StatusOK, mod.UI)
}

// handleGetProviderAsset proxies a module-owned UI asset, verifying it against
// the descriptor before it is served.
func (s *moduleAPIServer) handleGetProviderAsset(w http.ResponseWriter, r *http.Request) {
	vars := gmux.Vars(r)
	if !s.acceptsAgent(vars["agent-id"]) {
		http.Error(w, "unknown agent id", http.StatusNotFound)
		return
	}
	mod, ok := s.module(vars["module-id"])
	if !ok {
		http.Error(w, "module not found", http.StatusNotFound)
		return
	}
	if len(mod.UI) == 0 {
		http.Error(w, "module has no UI", http.StatusNotFound)
		return
	}

	assetPath := "/" + vars["path"]
	if vars["path"] == "" {
		assetPath = "/"
	}
	declared := declaredAssets(mod.UI)
	relPath := strings.TrimPrefix(assetPath, "/")
	asset, ok := moduleapi.FindDeclaredAsset(declared, relPath)
	if !ok {
		asset, ok = moduleapi.FindDeclaredAsset(declared, strings.TrimPrefix(relPath, "ui/"))
	}
	if !ok {
		http.Error(w, "asset is not declared by the module", http.StatusNotFound)
		return
	}

	validated, err := agentmod.ValidateOpenAPI(mod.OpenAPIYAML)
	if err != nil {
		http.Error(w, "module contract unavailable", http.StatusServiceUnavailable)
		return
	}
	// The asset operation is the module's own HTTP route for the file.
	opPath := assetPath
	if _, ok := validated.MatchOperation(http.MethodGet, opPath); !ok {
		opPath = "/ui" + assetPath
		if _, ok := validated.MatchOperation(http.MethodGet, opPath); !ok {
			http.Error(w, fmt.Sprintf("asset operation not found for path: %s", assetPath), http.StatusNotFound)
			return
		}
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, opPath, nil)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}
	req.Header.Set(moduleapi.CorrelationHeader, moduleapi.NewCorrelationID())

	resp, err := s.invoker.InvokeLocal(r.Context(), mod.ModuleID, mod.Revision, req)
	if err != nil {
		http.Error(w, fmt.Sprintf("asset fetch failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("module returned status %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	// Bound the read at one byte past the declared size, then verify.
	data := make([]byte, 0, asset.Size+1)
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if int64(len(data)+n) > asset.Size+1 {
				http.Error(w, "asset larger than declared", http.StatusBadGateway)
				return
			}
			data = append(data, buf[:n]...)
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("agent: module api: provider asset %s read failed: %v", assetPath, err)
				http.Error(w, "asset read failed", http.StatusBadGateway)
				return
			}
			break
		}
	}
	if err := moduleapi.VerifyAssetContent(data, asset); err != nil {
		log.Printf("agent: module api: provider asset %s failed verification: %v", asset.Path, err)
		http.Error(w, "asset failed verification", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", moduleapi.ContentTypeForPath(asset.Path))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", asset.Size))
	w.Header().Set("X-Asset-Digest", asset.Digest)
	w.Header().Set("X-Module-Revision", mod.Revision)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if strings.HasSuffix(asset.Path, ".svg") {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	}
	w.Write(data)
}

// handleGetConsumerAsset serves a consumer UI asset from an installed package.
//
// Consumer UI packages are normally installed into the console's trusted store
// on the server (ADR-004); the agent only answers when a package it has on disk
// declares the same package digest, and otherwise says so plainly.
func (s *moduleAPIServer) handleGetConsumerAsset(w http.ResponseWriter, r *http.Request) {
	vars := gmux.Vars(r)
	digest := vars["package-digest"]
	relPath := vars["path"]
	if digest == "" || relPath == "" {
		http.Error(w, "package digest and path required", http.StatusBadRequest)
		return
	}

	pkg, ok := s.packageByUIDigest(digest)
	if !ok {
		http.Error(w, "consumer UI package not installed", http.StatusNotFound)
		return
	}
	asset, ok := moduleapi.FindDeclaredAsset(moduleapi.DeclaredAssetsFromSDK(pkg.UIDescriptors), relPath)
	if !ok {
		http.Error(w, "asset is not declared by the package", http.StatusNotFound)
		return
	}
	data, err := moduleapi.ReadVerifiedAsset(pkg.PackageDir, asset)
	if err != nil {
		log.Printf("agent: module api: consumer asset %s: %v", asset.Path, err)
		status := http.StatusBadGateway
		if errors.Is(err, moduleapi.ErrUnsafeAssetPath) || errors.Is(err, moduleapi.ErrAssetNotDeclared) {
			status = http.StatusNotFound
		}
		http.Error(w, "asset unavailable", status)
		return
	}

	w.Header().Set("Content-Type", moduleapi.ContentTypeForPath(asset.Path))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", asset.Size))
	w.Header().Set("X-Asset-Digest", asset.Digest)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if strings.HasSuffix(asset.Path, ".svg") {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	}
	w.Write(data)
}

// packageByUIDigest finds an installed package carrying a consumer UI
// descriptor for this package digest.
func (s *moduleAPIServer) packageByUIDigest(digest string) (*agentmod.Package, bool) {
	if s.registry == nil {
		return nil, false
	}
	for _, id := range s.registry.AllModules() {
		pkg := s.registry.Package(id)
		if pkg == nil {
			continue
		}
		for _, desc := range pkg.UIDescriptors {
			if desc.Kind != sdk.UIConsumer || desc.PackageDigest == "" {
				continue
			}
			if strings.EqualFold(desc.PackageDigest, digest) {
				return pkg, true
			}
		}
	}
	return nil, false
}

// declaredAssets flattens protocol UI entries into the wire-neutral asset form.
func declaredAssets(ui []moduleproto.UIEntry) []moduleapi.DeclaredAsset {
	var out []moduleapi.DeclaredAsset
	for _, entry := range ui {
		for _, asset := range entry.AssetPaths {
			out = append(out, moduleapi.DeclaredAsset{Path: asset.Path, Size: asset.Size, Digest: asset.Digest})
		}
	}
	return out
}
