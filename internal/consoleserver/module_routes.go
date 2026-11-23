package consoleserver

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	gmux "github.com/gorilla/mux"
	"github.com/wseternal/ssetunnel/internal/agentmod"
	"github.com/wseternal/ssetunnel/internal/moduleapi"
	"github.com/wseternal/ssetunnel/internal/server"
)

// moduleRoutes adds module discovery and invocation routes to the console API.
// These routes are mounted before the SPA catch-all and require authentication.
type moduleRoutes struct {
	server *server.Server
}

// newModuleRoutes creates the module route handlers.
func newModuleRoutes(srv *server.Server) *moduleRoutes {
	return &moduleRoutes{server: srv}
}

// handleListModules returns all modules for agents accessible to the current user.
// GET /api/v1/modules?agent=<agent-id>
func (m *moduleRoutes) handleListModules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessInfo := server.UserSessionFromContext(r)
	if sessInfo == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	agentID := r.URL.Query().Get("agent")
	if agentID == "" {
		http.Error(w, "agent parameter required", http.StatusBadRequest)
		return
	}

	// Find the agent session for this user.
	session := m.server.FindSessionByAgent(agentID, sessInfo)
	if session == nil {
		http.Error(w, "agent not found or not accessible", http.StatusNotFound)
		return
	}

	// Get the catalog for this session.
	catalog := session.ModuleCatalog()
	if catalog == nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	modules := catalog.ListModules()
	result := make([]moduleSummary, 0, len(modules))
	for _, mod := range modules {
		result = append(result, moduleSummary{
			ModuleID:       mod.ModuleID,
			ServiceName:    mod.ServiceName,
			ServiceVersion: mod.ServiceVersion,
			Revision:       mod.Revision,
			Health:         mod.Health.Status,
			HasUI:          len(mod.UI) > 0,
		})
	}

	// Include catalog epoch in response header so consumers can detect stale epochs.
	if epoch := catalog.Epoch(); epoch != "" {
		w.Header().Set("X-Catalog-Epoch", epoch)
	}
	writeJSON(w, http.StatusOK, result)
}

// handleGetModule returns details for a specific module.
// GET /api/v1/modules/<agent-id>/<module-id>
func (m *moduleRoutes) handleGetModule(w http.ResponseWriter, r *http.Request) {
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

	result := moduleDetail{
		ModuleID:       mod.ModuleID,
		ServiceName:    mod.ServiceName,
		ServiceVersion: mod.ServiceVersion,
		Revision:       mod.Revision,
		Health:         mod.Health.Status,
		HealthMessage:  mod.Health.Message,
		CheckedAt:      time.UnixMilli(mod.Health.CheckedAt).Format(time.RFC3339),
		OperationCount: len(mod.ValidatedOpenAPI.Operations),
		HasUI:          len(mod.UI) > 0,
	}

	writeJSON(w, http.StatusOK, result)
}

// handleGetOpenAPI returns the OpenAPI YAML for a module.
// GET /api/v1/modules/openapi/<agent-id>/<module-id>
func (m *moduleRoutes) handleGetOpenAPI(w http.ResponseWriter, r *http.Request) {
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

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("X-Module-Revision", mod.Revision)
	w.Write(mod.OpenAPIYAML)
}

// moduleOperationsResponse is the payload consumed by the console's generic
// module API workbench. Operations are rendered as forms, so they carry
// parameters and a flattened request-body schema rather than raw YAML.
type moduleOperationsResponse struct {
	ModuleID   string                  `json:"module_id"`
	Revision   string                  `json:"revision,omitempty"`
	Title      string                  `json:"title"`
	Operations []agentmod.APIOperation `json:"operations"`
}

// handleGetOperationModels returns the invocation model for a module, derived
// on demand from the OpenAPI document already stored in the catalog.
// GET /api/v1/modules/<agent-id>/<module-id>/operations
func (m *moduleRoutes) handleGetOperationModels(w http.ResponseWriter, r *http.Request) {
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

	title, operations, err := agentmod.BuildOperationModels(mod.OpenAPIYAML)
	if err != nil {
		// The document was validated at catalog commit, so this signals a
		// contract the model builder cannot understand rather than user error.
		log.Printf("module %s: build operation models: %v", moduleID, err)
		http.Error(w, "module OpenAPI document cannot be rendered", http.StatusInternalServerError)
		return
	}

	w.Header().Set("X-Module-Revision", mod.Revision)
	writeJSON(w, http.StatusOK, moduleOperationsResponse{
		ModuleID:   mod.ModuleID,
		Revision:   mod.Revision,
		Title:      title,
		Operations: operations,
	})
}

// handleInvoke invokes a module operation.
// POST /api/v1/modules/invoke/<agent-id>/<module-id>/<operation-path...>
func (m *moduleRoutes) handleInvoke(w http.ResponseWriter, r *http.Request) {
	// An OPTIONS with Access-Control-Request-Method is a CORS preflight and is
	// answered here. A plain OPTIONS is an operation and must reach a module
	// whose contract declares it (see IsPreflight).
	if moduleapi.IsPreflight(r) {
		// W4 fix: Do not reflect arbitrary Origin as Access-Control-Allow-Origin.
		// Only allow same-origin requests (no CORS headers needed for same-origin).
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, HEAD, OPTIONS, TRACE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Correlation-ID")
		w.WriteHeader(http.StatusNoContent)
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

	// fetch() refuses to send TRACE, so the workbench sends POST with a
	// method override. Resolve it before operation matching and before the
	// request is built (BuildRequest strips the override header).
	method, _ := moduleapi.ResolveMethod(r.Method, r.Header.Get(moduleapi.MethodOverrideHeader))

	// Take the operation path from the escaped URL: router variables are
	// decoded, so a path parameter that legitimately contains %2F would be
	// split into two segments and stop matching its template.
	operationPath := "/" + vars["path"]
	if vars["path"] == "" {
		operationPath = "/"
	}
	if escaped, ok := moduleapi.TrailAfterSegments(r.URL.EscapedPath(), "/api/v1/modules/invoke/", 2); ok {
		operationPath = escaped
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

	// Validate operation exists in OpenAPI.
	op, ok := mod.ValidatedOpenAPI.MatchOperation(method, operationPath)
	if !ok {
		http.Error(w, fmt.Sprintf("operation not found: %s %s", method, operationPath), http.StatusNotFound)
		return
	}

	// Request invocation from stream handler.
	streamHandler := session.ModuleStreamHandler()
	if streamHandler == nil {
		http.Error(w, "module invocation not available", http.StatusServiceUnavailable)
		return
	}

	// Build the invocation request. BuildRequest keeps the caller's escaped
	// operation path and RawQuery, strips surface credentials, and attaches a
	// correlation ID.
	invReq, err := moduleapi.BuildRequest(r.Context(), r, method, operationPath)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	log.Printf("invoke: %s %s on module %s (operation=%s, correlation=%s)",
		method, operationPath, moduleID, op.OperationID, invReq.Header.Get(moduleapi.CorrelationHeader))

	// Request the stream.
	stream, invocationID, err := streamHandler.RequestInvocation(r.Context(), moduleID, mod.Revision, invReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("invocation failed: %v", err), http.StatusBadGateway)
		return
	}
	defer streamHandler.CompleteInvocation(invocationID)

	// Write the request to the stream.
	if err := invReq.Write(stream); err != nil {
		http.Error(w, "failed to write request", http.StatusBadGateway)
		return
	}

	// Read the response from the stream.
	// Use the existing buffered reader if available (N2 fix: avoid double buffering).
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

	// W5 fix: Copy response headers using an allowlist (not blocklist).
	//
	// The allowlist lives in moduleapi so every surface applies the same rule.
	// cache-control is deliberately absent. This route returns output from a
	// user's own agent behind UserSessionMiddleware, the URL carries no user
	// identity and nothing sets Vary, so honouring a module's
	// "public, max-age=86400" would let a shared cache answer another user's
	// invocation from that URL. The router's no-store floor is kept instead.
	moduleapi.CopyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set(moduleapi.CorrelationHeader, invReq.Header.Get(moduleapi.CorrelationHeader))
	w.WriteHeader(resp.StatusCode)

	// Stream the body (W12 fix: log copy errors).
	if err := moduleapi.CopyStream(w, resp.Body); err != nil {
		log.Printf("invoke: copy response body: %v", err)
	}
}

// Helper types for JSON responses.
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

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
