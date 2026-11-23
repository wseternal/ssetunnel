package consoleserver

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	gmux "github.com/gorilla/mux"
	"github.com/wseternal/ssetunnel/internal/moduleapi"
	"github.com/wseternal/ssetunnel/internal/moduleui"
	"github.com/wseternal/ssetunnel/internal/server"
)

// moduleUIRoutes handles serving module UI assets through the console.
// Provider UI assets are fetched via invocation to the module's declared
// asset operation. Consumer UI assets are served from the local trusted
// package store.
type moduleUIRoutes struct {
	server  *server.Server
	uiStore *moduleui.Store
}

// newModuleUIRoutes creates the module UI route handlers.
func newModuleUIRoutes(srv *server.Server, uiStore *moduleui.Store) *moduleUIRoutes {
	return &moduleUIRoutes{
		server:  srv,
		uiStore: uiStore,
	}
}

// handleGetConsumerAsset serves a cached consumer UI asset.
// GET /api/v1/modules/ui/consumer/{package-digest}/{path:.*}
//
// Consumer assets are served from the local trusted package store.
// Every request verifies the package is still trusted and the asset
// digest matches the manifest.
func (m *moduleUIRoutes) handleGetConsumerAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessInfo := server.UserSessionFromContext(r)
	if sessInfo == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	vars := gmux.Vars(r)
	packageDigest := vars["package-digest"]
	relPath := vars["path"]

	if packageDigest == "" || relPath == "" {
		http.Error(w, "package digest and path required", http.StatusBadRequest)
		return
	}

	// Verify path is safe (no traversal).
	if strings.Contains(relPath, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	asset, err := m.uiStore.GetAsset(packageDigest, relPath)
	if err != nil {
		if err == moduleui.ErrPackageNotFound {
			http.Error(w, "package not found or not trusted", http.StatusNotFound)
			return
		}
		if err == moduleui.ErrAssetNotFound {
			http.Error(w, "asset not found", http.StatusNotFound)
			return
		}
		log.Printf("module ui: get asset error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Set content type based on extension.
	contentType := guessContentType(relPath)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", asset.Size))
	w.Header().Set("X-Asset-Digest", asset.Digest)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Write(asset.Data)
}

// handleGetProviderAsset proxies a request to a module's declared asset operation.
// GET /api/v1/modules/ui/provider/{agent-id}/{module-id}/{path:.*}
//
// Provider assets are fetched by invoking the module's asset operation
// (declared in OpenAPI). The response is verified against the manifest
// before serving to the browser.
func (m *moduleUIRoutes) handleGetProviderAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessInfo := server.UserSessionFromContext(r)
	if sessInfo == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	vars := gmux.Vars(r)
	agentID := vars["agent-id"]
	moduleID := vars["module-id"]
	assetPath := "/" + vars["path"]
	if vars["path"] == "" {
		assetPath = "/"
	}

	session := m.server.FindSessionByAgent(agentID, sessInfo)
	if session == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	catalog := session.ModuleCatalog()
	if catalog == nil {
		http.Error(w, "no catalog available", http.StatusServiceUnavailable)
		return
	}

	mod := catalog.GetModule(moduleID)
	if mod == nil {
		http.Error(w, "module not found", http.StatusNotFound)
		return
	}

	// Check if module has UI entries with asset operations.
	if len(mod.UI) == 0 {
		http.Error(w, "module has no UI", http.StatusNotFound)
		return
	}

	// The descriptor's asset list is the contract. A path it does not declare
	// is not servable, whatever the module would answer for it — otherwise a
	// module could name any file it likes through the proxy.
	var declared []moduleapi.DeclaredAsset
	for _, ui := range mod.UI {
		for _, a := range ui.AssetPaths {
			declared = append(declared, moduleapi.DeclaredAsset{Path: a.Path, Size: a.Size, Digest: a.Digest})
		}
	}
	relPath := strings.TrimPrefix(assetPath, "/")
	asset, ok := moduleapi.FindDeclaredAsset(declared, relPath)
	if !ok {
		// A provider package may declare its files under "ui/"; the asset URL
		// is the path relative to the package, so retry without that prefix.
		asset, ok = moduleapi.FindDeclaredAsset(declared, strings.TrimPrefix(relPath, "ui/"))
	}
	if !ok {
		http.Error(w, "asset is not declared by the module", http.StatusNotFound)
		return
	}

	// Find the concrete asset path the module serves. `MatchOperation` matches a
	// concrete path against an OpenAPI template, but the returned operation's
	// Path is still the template (e.g. "/assets/{path}"); sending that would ask
	// the module for the literal file "{path}". Keep the candidate path that
	// matched, and use the operation only for validation/logging.
	opPath := assetPath
	op, ok := mod.ValidatedOpenAPI.MatchOperation("GET", opPath)
	if !ok {
		// A package may declare its asset files under "ui/".
		opPath = "/ui" + assetPath
		op, ok = mod.ValidatedOpenAPI.MatchOperation("GET", opPath)
		if !ok {
			http.Error(w, fmt.Sprintf("asset operation not found for path: %s", assetPath), http.StatusNotFound)
			return
		}
	}

	// Request invocation. The candidate path, not the operation template, is
	// what the module serves.
	streamHandler := session.ModuleStreamHandler()
	if streamHandler == nil {
		http.Error(w, "invocation not available", http.StatusServiceUnavailable)
		return
	}

	invReq, err := http.NewRequestWithContext(r.Context(), "GET", opPath, nil)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}
	invReq.Header.Set(moduleapi.CorrelationHeader, moduleapi.NewCorrelationID())

	log.Printf("module ui: fetching provider asset %s from %s/%s (op=%s)",
		assetPath, agentID, moduleID, op.OperationID)

	stream, invocationID, err := streamHandler.RequestInvocation(r.Context(), moduleID, mod.Revision, invReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("asset fetch failed: %v", err), http.StatusBadGateway)
		return
	}
	defer streamHandler.CompleteInvocation(invocationID)

	// Write the request.
	if err := invReq.Write(stream); err != nil {
		http.Error(w, "failed to write request", http.StatusBadGateway)
		return
	}

	// Read response (use the existing buffered reader if available; N2 fix).
	var reader *bufio.Reader
	if br, ok := stream.(interface{ BufioReader() *bufio.Reader }); ok {
		reader = br.BufioReader()
	} else {
		reader = bufio.NewReader(stream)
	}
	resp, err := http.ReadResponse(reader, invReq)
	if err != nil {
		http.Error(w, "failed to read response", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("module returned status %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	// Read the body, bounded by one byte past the declared size so an oversized
	// response is rejected rather than buffered.
	data := make([]byte, 0, min(asset.Size, moduleui.MaxAssetSize)+1)
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
				log.Printf("module ui: provider asset %s read failed: %v", assetPath, err)
				http.Error(w, "asset read failed", http.StatusBadGateway)
				return
			}
			break
		}
	}

	// Verify the bytes against the declaration before they reach the browser.
	if err := moduleapi.VerifyAssetContent(data, asset); err != nil {
		log.Printf("module ui: provider asset %s failed verification: %v", asset.Path, err)
		http.Error(w, "asset failed verification", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", moduleapi.ContentTypeForPath(asset.Path))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", asset.Size))
	w.Header().Set("X-Asset-Digest", asset.Digest)
	w.Header().Set("X-Module-Revision", mod.Revision)
	w.Header().Set("Cache-Control", "private, max-age=3600") // 1 hour for provider assets
	// W9 fix: Add restrictive CSP for SVG assets to prevent XSS.
	if strings.HasSuffix(asset.Path, ".svg") {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	}
	w.Write(data)
}

// handleListModuleUI returns the UI descriptors for a module.
// GET /api/v1/modules/ui/{agent-id}/{module-id}
func (m *moduleUIRoutes) handleListModuleUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessInfo := server.UserSessionFromContext(r)
	if sessInfo == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	vars := gmux.Vars(r)
	agentID := vars["agent-id"]
	moduleID := vars["module-id"]

	session := m.server.FindSessionByAgent(agentID, sessInfo)
	if session == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	catalog := session.ModuleCatalog()
	if catalog == nil {
		http.Error(w, "no catalog available", http.StatusServiceUnavailable)
		return
	}

	mod := catalog.GetModule(moduleID)
	if mod == nil {
		http.Error(w, "module not found", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, mod.UI)
}

// guessContentType returns a MIME type based on file extension. The shared
// implementation lives in moduleapi so the console and the agent's loopback
// API answer with the same type for the same asset.
func guessContentType(p string) string {
	return moduleapi.ContentTypeForPath(p)
}
