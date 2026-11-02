package consoleserver

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	gmux "github.com/gorilla/mux"
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
// GET /console/api/v1/modules?agent=<agent-id>
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
	session := m.server.FindSessionByAgent(agentID, sessInfo.UserID)
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

	writeJSON(w, http.StatusOK, result)
}

// handleGetModule returns details for a specific module.
// GET /console/api/v1/modules/<agent-id>/<module-id>
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

	session := m.server.FindSessionByAgent(agentID, sessInfo.UserID)
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
// GET /console/api/v1/modules/openapi/<agent-id>/<module-id>
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

	session := m.server.FindSessionByAgent(agentID, sessInfo.UserID)
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

// handleInvoke invokes a module operation.
// POST /console/api/v1/modules/invoke/<agent-id>/<module-id>/<operation-path...>
func (m *moduleRoutes) handleInvoke(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		// W4 fix: No CORS wildcard on invoke endpoint (requires explicit origin).
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
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
	operationPath := "/" + vars["path"]
	if vars["path"] == "" {
		operationPath = "/"
	}

	session := m.server.FindSessionByAgent(agentID, sessInfo.UserID)
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
	op, ok := mod.ValidatedOpenAPI.MatchOperation(r.Method, operationPath)
	if !ok {
		http.Error(w, fmt.Sprintf("operation not found: %s %s", r.Method, operationPath), http.StatusNotFound)
		return
	}

	// Request invocation from stream handler.
	streamHandler := session.ModuleStreamHandler()
	if streamHandler == nil {
		http.Error(w, "module invocation not available", http.StatusServiceUnavailable)
		return
	}

	// Build the invocation request.
	invReq, err := http.NewRequestWithContext(r.Context(), r.Method, operationPath, r.Body)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	// Copy headers (excluding auth and hop-by-hop).
	for key, values := range r.Header {
		if isHopByHopHeader(key) || isAuthHeader(key) {
			continue
		}
		for _, v := range values {
			invReq.Header.Add(key, v)
		}
	}

	// Add correlation ID if not present.
	if invReq.Header.Get("X-Correlation-ID") == "" {
		invReq.Header.Set("X-Correlation-ID", generateCorrelationID())
	}

	log.Printf("invoke: %s %s on module %s (operation=%s, correlation=%s)",
		r.Method, operationPath, moduleID, op.OperationID, invReq.Header.Get("X-Correlation-ID"))

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
	// Use a custom reader that doesn't buffer the entire body.
	resp, err := http.ReadResponse(bufio.NewReader(stream), invReq)
	if err != nil {
		http.Error(w, "failed to read response", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers (W6 fix: filter sensitive headers).
	for key, values := range resp.Header {
		// Skip hop-by-hop and sensitive headers.
		lowerKey := strings.ToLower(key)
		if lowerKey == "set-cookie" || lowerKey == "connection" || lowerKey == "transfer-encoding" {
			continue
		}
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.Header().Set("X-Correlation-ID", invReq.Header.Get("X-Correlation-ID"))
	w.WriteHeader(resp.StatusCode)

	// Stream the body.
	io.Copy(w, resp.Body)
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

// isHopByHopHeader checks if a header is a hop-by-hop header that should not be forwarded.
func isHopByHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailers", "transfer-encoding", "upgrade":
		return true
	}
	return false
}

// isAuthHeader checks if a header is an authentication header that should be stripped.
func isAuthHeader(key string) bool {
	switch strings.ToLower(key) {
	case "authorization", "cookie", "x-sset-token":
		return true
	}
	return false
}

// generateCorrelationID creates a unique correlation ID.
func generateCorrelationID() string {
	var b [8]byte
	rand.Read(b[:])
	return fmt.Sprintf("corr-%x", b[:])
}

